package payloadcodec

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"google.golang.org/protobuf/proto"
)

func TestSmallPayloadsPassThrough(t *testing.T) {
	dc := DataConverter()
	p, err := dc.ToPayload("hello")
	require.NoError(t, err)
	require.Equal(t, "json/plain", string(p.Metadata["encoding"]))
	var s string
	require.NoError(t, dc.FromPayload(p, &s))
	require.Equal(t, "hello", s)
}

func TestLargePayloadIsCompressedAndRoundTrips(t *testing.T) {
	dc := DataConverter()
	text := strings.Repeat("A pedestrian was struck on Colorado Boulevard. ", 2000)
	p, err := dc.ToPayload(text)
	require.NoError(t, err)
	require.Equal(t, Encoding, string(p.Metadata["encoding"]))
	require.Less(t, len(p.Data), len(text)/10)
	var back string
	require.NoError(t, dc.FromPayload(p, &back))
	require.Equal(t, text, back)
}

// The fixture is produced by the Python codec; decoding it proves the two
// implementations agree on the wire format.
func TestDecodesPayloadEncodedByPython(t *testing.T) {
	b, err := os.ReadFile("../../crawler/tests/fixtures/codec_interop.json")
	require.NoError(t, err)
	var fx struct{ Payload, Plain string }
	require.NoError(t, json.Unmarshal(b, &fx))
	raw, err := hex.DecodeString(fx.Payload)
	require.NoError(t, err)
	var p commonpb.Payload
	require.NoError(t, proto.Unmarshal(raw, &p))
	require.Equal(t, Encoding, string(p.Metadata["encoding"]))
	var s string
	require.NoError(t, DataConverter().FromPayload(&p, &s))
	require.Equal(t, fx.Plain, s)
}
