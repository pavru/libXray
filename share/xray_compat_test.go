package share

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xtls/xray-core/infra/conf"
)

// setStreamMethodForTest selects a transport through streamSettings.method,
// or through network on cores that predate the method field.
func setStreamMethodForTest(stream *conf.StreamConfig, name string) {
	protocol := conf.TransportProtocol(name)
	if field := reflect.ValueOf(stream).Elem().FieldByName("Method"); field.IsValid() {
		field.Set(reflect.ValueOf(&protocol))
		return
	}
	stream.Network = &protocol
}

func TestStreamMethodFollowsTheLinkedCore(t *testing.T) {
	stream := &conf.StreamConfig{}
	assert.Nil(t, streamMethod(stream))
	setStreamMethodForTest(stream, "xhttp")
	if _, ok := reflect.TypeOf(conf.StreamConfig{}).FieldByName("Method"); ok {
		require.NotNil(t, streamMethod(stream))
		assert.Equal(t, conf.TransportProtocol("xhttp"), *streamMethod(stream))
	} else {
		assert.Nil(t, streamMethod(stream))
	}
}

// Port hopping must round-trip in the representation the linked core builds.
func TestHysteria2HopUsesTheLinkedCoreFormat(t *testing.T) {
	input := "hy2://auth@host:5000-5002,6000?hop-interval=10&up=50%20mbps#Node"
	config, err := convertShareLinksWithKeyForTest(input, "")
	require.NoError(t, err)
	stream := config.OutboundConfigs[0].StreamSetting
	mask := stream.FinalMask
	require.NotNil(t, mask)
	assert.Equal(t, conf.Bandwidth("50 mbps"), mask.QuicParams.BrutalUp)
	if legacyUDPHop {
		assert.Empty(t, mask.Udp)
		ports, interval, ok := legacyHysteria2Hop(mask)
		require.True(t, ok)
		assert.Equal(t, "5000-5002,6000", ports.String())
		assert.Equal(t, int32(10), interval.From)
	} else {
		require.Len(t, mask.Udp, 1)
		assert.Equal(t, "udphop", mask.Udp[0].Type)
		_, _, ok := legacyHysteria2Hop(mask)
		assert.False(t, ok)
	}
	_, err = stream.Build()
	require.NoError(t, err)

	link, err := shareLink(config.OutboundConfigs[0])
	require.NoError(t, err)
	assert.Equal(t, "host:5000-5002,6000", link.Host)
	assert.Equal(t, "10", link.Query().Get("hop-interval"))

	// Bandwidth is client-local and not exported, so compare without it.
	expected, err := ConvertShareLinksToXrayJson("hy2://auth@host:5000-5002,6000?hop-interval=10#Node", "")
	require.NoError(t, err)
	again, err := ConvertShareLinksToXrayJson(link.String(), "")
	require.NoError(t, err)
	assert.JSONEq(t, string(expected), string(again))
}
