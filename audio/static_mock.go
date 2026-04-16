//go:build !pi

package audio

// Static is a no-op implementation used on non-Pi builds where ALSA is not
// available. All methods are safe to call and do nothing.
type Static struct{}

func NewStatic(_ Config) *Static { return &Static{} }

func (s *Static) Start()            {}
func (s *Static) Stop()             {}
func (s *Static) IsPlaying() bool   { return false }
func (s *Static) SetGain(_ float64) {}
func (s *Static) Shuffle()          {}
