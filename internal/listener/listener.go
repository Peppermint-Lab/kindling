// Package listener streams PostgreSQL WAL changes via logical replication
// and dispatches entity IDs to the appropriate reconciler schedulers.
//
// It uses pglogrepl to establish a logical replication connection, create a
// temporary replication slot, and stream changes from a publication covering
// the tables that drive reconciliation (deployments, builds, vms, domains, servers).
//
// When a row changes, the listener extracts the entity's UUID and calls the
// registered handler, which typically schedules the entity's reconciler.
package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"
)

// Handler is called when a table row changes. The UUID is the entity's primary key.
type Handler func(ctx context.Context, id uuid.UUID)

// Config holds the listener configuration.
type Config struct {
	// DatabaseURL is the PostgreSQL connection string.
	// The listener appends replication=database automatically.
	DatabaseURL string

	// SlotName is the replication slot name. When empty, a unique temporary
	// slot name is generated so multiple listeners can run concurrently.
	SlotName string

	// PublicationName is the PG publication name (default: "kindling_changes").
	PublicationName string

	// Handlers for each table.
	OnDeployment             Handler
	OnDeploymentInstance     Handler
	OnProject                Handler
	OnBuild                  Handler
	OnVM                     Handler
	OnDomain                 Handler
	OnServer                 Handler
	OnInstanceMigration      Handler
	OnProjectVolumeOperation Handler
}

// Listener streams PostgreSQL WAL changes and dispatches to handlers.
type Listener struct {
	cfg       Config
	conn      *pgconn.PgConn
	relations map[uint32]*pglogrepl.RelationMessageV2
	typeMap   *pgtype.Map
	clientLSN pglogrepl.LSN
	inStream  bool

	standbyTimeout time.Duration
}

// New creates a new WAL listener.
func New(cfg Config) *Listener {
	if cfg.SlotName == "" {
		cfg.SlotName = defaultSlotName()
	}
	if cfg.PublicationName == "" {
		cfg.PublicationName = "kindling_changes"
	}

	return &Listener{
		cfg:            cfg,
		relations:      make(map[uint32]*pglogrepl.RelationMessageV2),
		typeMap:        pgtype.NewMap(),
		standbyTimeout: 10 * time.Second,
	}
}

var publicationTables = []string{
	"deployments",
	"deployment_instances",
	"projects",
	"builds",
	"vms",
	"domains",
	"servers",
	"preview_environments",
	"instance_migrations",
	"project_volume_operations",
}

func defaultSlotName() string {
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return "kindling_listener_" + suffix
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func publicationCreateSQL(name string) string {
	return fmt.Sprintf(
		"CREATE PUBLICATION %s FOR TABLE %s",
		quoteIdentifier(name),
		strings.Join(publicationTables, ", "),
	)
}

func publicationAlterSQL(name string) string {
	return fmt.Sprintf(
		"ALTER PUBLICATION %s SET TABLE %s",
		quoteIdentifier(name),
		strings.Join(publicationTables, ", "),
	)
}

func isDuplicateObjectError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42710"
}

// Start connects to PostgreSQL in replication mode and streams WAL changes
// until the context is cancelled. This method blocks.
func (l *Listener) Start(ctx context.Context) error {
	slog.Info("starting WAL listener")

	connStr := replicationURL(l.cfg.DatabaseURL)
	conn, err := pgconn.Connect(ctx, connStr)
	if err != nil {
		return fmt.Errorf("WAL listener connect: %w", err)
	}
	l.conn = conn
	defer func() {
		l.conn.Close(context.Background())
		l.conn = nil
	}()

	if err := l.setupPublication(ctx); err != nil {
		return fmt.Errorf("WAL listener setup publication: %w", err)
	}

	if err := l.createSlot(ctx); err != nil {
		return fmt.Errorf("WAL listener create slot: %w", err)
	}

	if err := l.startReplication(ctx); err != nil {
		return fmt.Errorf("WAL listener start replication: %w", err)
	}

	return l.loop(ctx)
}

// setupPublication ensures the publication exists for the tracked tables.
func (l *Listener) setupPublication(ctx context.Context) error {
	result := l.conn.Exec(ctx, publicationCreateSQL(l.cfg.PublicationName))
	if _, err := result.ReadAll(); err != nil {
		if !isDuplicateObjectError(err) {
			return fmt.Errorf("create publication: %w", err)
		}

		result = l.conn.Exec(ctx, publicationAlterSQL(l.cfg.PublicationName))
		if _, err := result.ReadAll(); err != nil {
			return fmt.Errorf("alter publication: %w", err)
		}

		slog.Info("updated publication", "name", l.cfg.PublicationName)
		return nil
	}

	slog.Info("created publication", "name", l.cfg.PublicationName)
	return nil
}

// createSlot creates a temporary logical replication slot.
// Temporary slots are automatically cleaned up when the connection closes.
func (l *Listener) createSlot(ctx context.Context) error {
	sysident, err := pglogrepl.IdentifySystem(ctx, l.conn)
	if err != nil {
		return fmt.Errorf("identify system: %w", err)
	}

	l.clientLSN = sysident.XLogPos
	slog.Info("identified PG system",
		"system_id", sysident.SystemID,
		"timeline", sysident.Timeline,
		"xlog_pos", sysident.XLogPos,
	)

	_, err = pglogrepl.CreateReplicationSlot(
		ctx, l.conn, l.cfg.SlotName, "pgoutput",
		pglogrepl.CreateReplicationSlotOptions{Temporary: true},
	)
	if err != nil {
		return fmt.Errorf("create replication slot: %w", err)
	}

	slog.Info("created replication slot", "name", l.cfg.SlotName)
	return nil
}

// startReplication begins the logical replication stream.
func (l *Listener) startReplication(ctx context.Context) error {
	err := pglogrepl.StartReplication(
		ctx, l.conn, l.cfg.SlotName, l.clientLSN,
		pglogrepl.StartReplicationOptions{
			PluginArgs: []string{
				"proto_version '2'",
				fmt.Sprintf("publication_names '%s'", l.cfg.PublicationName),
				"messages 'true'",
				"streaming 'true'",
			},
		},
	)
	if err != nil {
		return fmt.Errorf("start replication: %w", err)
	}

	slog.Info("logical replication started", "slot", l.cfg.SlotName)
	return nil
}

// loop processes WAL messages until the context is cancelled.
func (l *Listener) loop(ctx context.Context) error {
	nextStandby := time.Now().Add(l.standbyTimeout)

	for {
		select {
		case <-ctx.Done():
			slog.Info("WAL listener stopped")
			return ctx.Err()
		default:
		}

		// Send standby status update periodically.
		if time.Now().After(nextStandby) {
			err := pglogrepl.SendStandbyStatusUpdate(ctx, l.conn,
				pglogrepl.StandbyStatusUpdate{WALWritePosition: l.clientLSN},
			)
			if err != nil {
				return fmt.Errorf("standby status update: %w", err)
			}
			nextStandby = time.Now().Add(l.standbyTimeout)
		}

		// Receive with deadline so we can send standby updates.
		msgCtx, cancel := context.WithDeadline(ctx, nextStandby)
		rawMsg, err := l.conn.ReceiveMessage(msgCtx)
		cancel()

		if err != nil {
			if pgconn.Timeout(err) {
				continue
			}
			return fmt.Errorf("receive message: %w", err)
		}

		if errMsg, ok := rawMsg.(*pgproto3.ErrorResponse); ok {
			return fmt.Errorf("PG error: %s (%s)", errMsg.Message, errMsg.Code)
		}

		msg, ok := rawMsg.(*pgproto3.CopyData)
		if !ok {
			continue
		}

		if err := l.processCopyData(ctx, msg); err != nil {
			slog.Error("failed to process WAL message", "error", err)
		}
	}
}

func (l *Listener) processCopyData(ctx context.Context, msg *pgproto3.CopyData) error {
	switch msg.Data[0] {
	case pglogrepl.PrimaryKeepaliveMessageByteID:
		pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
		if err != nil {
			return fmt.Errorf("parse keepalive: %w", err)
		}
		if pkm.ServerWALEnd > l.clientLSN {
			l.clientLSN = pkm.ServerWALEnd
		}

	case pglogrepl.XLogDataByteID:
		xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
		if err != nil {
			return fmt.Errorf("parse xlog data: %w", err)
		}
		if err := l.processWALData(ctx, xld.WALData); err != nil {
			return err
		}
		if xld.WALStart > l.clientLSN {
			l.clientLSN = xld.WALStart
		}
	}

	return nil
}

func (l *Listener) processWALData(ctx context.Context, data []byte) error {
	logicalMsg, err := pglogrepl.ParseV2(data, l.inStream)
	if err != nil {
		return fmt.Errorf("parse logical message: %w", err)
	}

	switch msg := logicalMsg.(type) {
	case *pglogrepl.RelationMessageV2:
		l.relations[msg.RelationID] = msg

	case *pglogrepl.InsertMessageV2:
		return l.dispatch(ctx, msg.RelationID, msg.Tuple)

	case *pglogrepl.UpdateMessageV2:
		return l.dispatch(ctx, msg.RelationID, msg.NewTuple)

	case *pglogrepl.StreamStartMessageV2:
		l.inStream = true

	case *pglogrepl.StreamStopMessageV2:
		l.inStream = false

	case *pglogrepl.StreamCommitMessageV2:
		// no-op

	case *pglogrepl.BeginMessage, *pglogrepl.CommitMessage, *pglogrepl.DeleteMessageV2:
		// no-op
	}

	return nil
}

// dispatch extracts the entity ID from a tuple and calls the appropriate handler.
func (l *Listener) dispatch(ctx context.Context, relationID uint32, tuple *pglogrepl.TupleData) error {
	rel, ok := l.relations[relationID]
	if !ok {
		return fmt.Errorf("unknown relation %d", relationID)
	}
	if tuple == nil {
		return nil
	}

	id, err := extractID(tuple, rel)
	if err != nil {
		return fmt.Errorf("extract ID from %s: %w", rel.RelationName, err)
	}

	switch rel.RelationName {
	case "deployments":
		if l.cfg.OnDeployment != nil {
			l.cfg.OnDeployment(ctx, id)
		}
	case "deployment_instances":
		if l.cfg.OnDeploymentInstance != nil {
			l.cfg.OnDeploymentInstance(ctx, id)
		}
	case "projects":
		if l.cfg.OnProject != nil {
			l.cfg.OnProject(ctx, id)
		}
	case "builds":
		if l.cfg.OnBuild != nil {
			l.cfg.OnBuild(ctx, id)
		}
	case "vms":
		if l.cfg.OnVM != nil {
			l.cfg.OnVM(ctx, id)
		}
	case "domains":
		if l.cfg.OnDomain != nil {
			l.cfg.OnDomain(ctx, id)
		}
	case "servers":
		if l.cfg.OnServer != nil {
			l.cfg.OnServer(ctx, id)
		}
	case "instance_migrations":
		if l.cfg.OnInstanceMigration != nil {
			l.cfg.OnInstanceMigration(ctx, id)
		}
	case "project_volume_operations":
		if l.cfg.OnProjectVolumeOperation != nil {
			l.cfg.OnProjectVolumeOperation(ctx, id)
		}
	}

	return nil
}

// extractID finds the "id" column in a tuple and parses it as a UUID.
func extractID(tuple *pglogrepl.TupleData, rel *pglogrepl.RelationMessageV2) (uuid.UUID, error) {
	for i, col := range tuple.Columns {
		if i >= len(rel.Columns) {
			continue
		}
		if rel.Columns[i].Name == "id" && col.Data != nil {
			return uuid.Parse(string(col.Data))
		}
	}
	return uuid.Nil, fmt.Errorf("id column not found")
}

// replicationURL appends replication=database to a PostgreSQL connection string.
func replicationURL(dbURL string) string {
	u, err := url.Parse(dbURL)
	if err != nil {
		if strings.Contains(dbURL, "?") {
			return dbURL + "&replication=database"
		}
		return dbURL + "?replication=database"
	}
	q := u.Query()
	q.Set("replication", "database")
	u.RawQuery = q.Encode()
	return u.String()
}
