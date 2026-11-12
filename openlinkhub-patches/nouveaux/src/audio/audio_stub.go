//go:build noaudio

package audio

// Stub sans PipeWire pour DSM: le moteur audio (casques) est desactive.

type Sink struct {
	Index  int    `json:"index"`
	Serial uint32 `json:"serial"`
	Name   string `json:"name"`
	Desc   string `json:"desc"`
}

type Audio struct {
	Enabled     bool   `json:"enabled"`
	SinkSerial  int    `json:"sinkSerial"`
	SinkName    string `json:"sinkName"`
	SinkDesc    string `json:"sinkDesc"`
	Latency     string `json:"latency"`
	MaxLatency  string `json:"maxLatency"`
	Rate        uint32 `json:"rate"`
	Channels    uint32 `json:"channels"`
	PollingRate uint32 `json:"pollingRate"`
	Debug       uint32 `json:"debug"`
	Frames      uint32 `json:"frames"`
}

var audio Audio

func Init()                             {}
func StopAudio()                        {}
func StartAudio()                       {}
func SetBand(band int, value float64)   {}
func GetSinks() []Sink                  { return nil }
func UpdateAudio(settings *Audio) uint8 { return 0 }
func UpdateTargetDevice(s *Audio) uint8 { return 0 }
func GetAudio() *Audio                  { return &audio }
