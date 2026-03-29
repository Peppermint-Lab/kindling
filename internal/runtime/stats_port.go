package runtime

// GuestStatsVsockPort is the guest vsock port where the guest-agent serves GET /stats.
// Must match cmd/guest-agent.
const GuestStatsVsockPort uint32 = 1026

// GuestControlVsockPort is the guest vsock port where the guest-agent serves
// command execution and file transfer operations.
const GuestControlVsockPort uint32 = 1028
