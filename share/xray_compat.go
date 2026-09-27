package share

import (
	"reflect"

	"github.com/xtls/xray-core/infra/conf"
)

// Xray-core changed two share-relevant configuration APIs after v26.6.27:
// streamSettings gained "method", and client UDP port hopping moved from
// finalmask.quicParams.udpHop to a "udphop" finalmask.udp entry. Both are
// resolved through reflection so libXray builds, and converts links in the
// linked core's own format, against either generation.

// legacyUDPHop reports whether the linked core configures port hopping in
// finalmask.quicParams.udpHop instead of a finalmask.udp entry.
var legacyUDPHop = func() bool {
	_, ok := reflect.TypeOf(conf.QuicParamsConfig{}).FieldByName("UdpHop")
	return ok
}()

// udpHopMask mirrors conf.UDPHop field by field, so the finalmask entry
// written for current cores is identical to marshaling conf.UDPHop.
type udpHopMask struct {
	Sockopt     *conf.SocketConfig `json:"sockopt"`
	Mode        string             `json:"mode"`
	Interval    conf.Int32Range    `json:"interval"`
	RemotePorts conf.PortList      `json:"remotePorts"`
	RemoteIPs   []string           `json:"remoteIPs"`
}

// streamMethod returns streamSettings.method, or nil when the linked core has
// no such field.
func streamMethod(stream *conf.StreamConfig) *conf.TransportProtocol {
	field := reflect.ValueOf(stream).Elem().FieldByName("Method")
	if !field.IsValid() || field.IsNil() {
		return nil
	}
	return field.Interface().(*conf.TransportProtocol)
}

// addHysteria2Hop configures client port hopping for the linked core.
func addHysteria2Hop(mask *conf.FinalMask, ports conf.PortList, interval int32) error {
	every := conf.Int32Range{Left: interval, Right: interval, From: interval, To: interval}
	if legacyUDPHop {
		if mask.QuicParams == nil {
			mask.QuicParams = &conf.QuicParamsConfig{}
		}
		hop := reflect.ValueOf(mask.QuicParams).Elem().FieldByName("UdpHop")
		hop.FieldByName("PortList").Set(reflect.ValueOf(ports))
		hop.FieldByName("Interval").Set(reflect.ValueOf(every))
		return nil
	}
	raw, err := convertJsonToRawMessage(&udpHopMask{
		Mode: "intervalLocal,intervalRemote", RemotePorts: ports, Interval: every,
	})
	if err != nil {
		return err
	}
	// The core wraps UDP masks in reverse order; hopping must wrap the raw socket.
	mask.Udp = append(mask.Udp, conf.Mask{Type: "udphop", Settings: &raw})
	return nil
}

// legacyHysteria2Hop returns the port hopping of a core that keeps it in
// quicParams; ok is false when the core or the mask configures none.
func legacyHysteria2Hop(mask *conf.FinalMask) (ports conf.PortList, interval conf.Int32Range, ok bool) {
	if !legacyUDPHop || mask.QuicParams == nil {
		return ports, interval, false
	}
	hop := reflect.ValueOf(mask.QuicParams).Elem().FieldByName("UdpHop")
	ports = hop.FieldByName("PortList").Interface().(conf.PortList)
	interval = hop.FieldByName("Interval").Interface().(conf.Int32Range)
	return ports, interval, len(ports.Range) > 0
}
