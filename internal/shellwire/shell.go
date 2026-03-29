package shellwire

import (
	"encoding/json"
	"io"
)

const ProtocolVersion = 1

type Frame struct {
	Version  int    `json:"version,omitempty"`
	Type     string `json:"type"`
	Data     string `json:"data,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type Encoder struct {
	enc *json.Encoder
}

func NewEncoder(w io.Writer) *Encoder {
	enc := json.NewEncoder(w)
	return &Encoder{enc: enc}
}

func (e *Encoder) Encode(frame Frame) error {
	if frame.Version == 0 {
		frame.Version = ProtocolVersion
	}
	return e.enc.Encode(frame)
}

type Decoder struct {
	dec *json.Decoder
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{dec: json.NewDecoder(r)}
}

func (d *Decoder) Decode() (Frame, error) {
	var frame Frame
	if err := d.dec.Decode(&frame); err != nil {
		return Frame{}, err
	}
	if frame.Version == 0 {
		frame.Version = ProtocolVersion
	}
	return frame, nil
}
