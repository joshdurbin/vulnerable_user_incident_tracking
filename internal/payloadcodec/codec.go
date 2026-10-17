// Package payloadcodec compresses large Temporal payloads (article text) with
// zlib so workflow history stays small. It is wire-compatible with
// crawler/src/collision_crawler/codec.py, which the Python worker uses.
// Payloads under Threshold pass through unchanged so they stay readable in the UI.
package payloadcodec

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"
)

const (
	Encoding  = "binary/zlib"
	Threshold = 1024
)

type Codec struct{}

var _ converter.PayloadCodec = Codec{}

func (Codec) Encode(in []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(in))
	for i, p := range in {
		raw, err := proto.Marshal(p)
		if err != nil {
			return nil, err
		}
		if len(raw) < Threshold {
			out[i] = p
			continue
		}
		var buf bytes.Buffer
		zw := zlib.NewWriter(&buf)
		if _, err := zw.Write(raw); err != nil {
			return nil, err
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		out[i] = &commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte(Encoding)}, Data: buf.Bytes()}
	}
	return out, nil
}

func (Codec) Decode(in []*commonpb.Payload) ([]*commonpb.Payload, error) {
	out := make([]*commonpb.Payload, len(in))
	for i, p := range in {
		if string(p.GetMetadata()["encoding"]) != Encoding {
			out[i] = p
			continue
		}
		zr, err := zlib.NewReader(bytes.NewReader(p.GetData()))
		if err != nil {
			return nil, fmt.Errorf("payloadcodec: %w", err)
		}
		raw, err := io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("payloadcodec: %w", err)
		}
		var orig commonpb.Payload
		if err := proto.Unmarshal(raw, &orig); err != nil {
			return nil, fmt.Errorf("payloadcodec: %w", err)
		}
		out[i] = &orig
	}
	return out, nil
}

// DataConverter is the default converter plus compression. Use it on every
// client and worker that touches the namespace.
func DataConverter() converter.DataConverter {
	return converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), Codec{})
}
