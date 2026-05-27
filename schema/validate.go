package schema

import (
	"errors"
	"log/slog"

	"github.com/frankbardon/parsec/envelope"
)

// Mode controls how a Validator handles envelopes whose payload does not
// conform to the registered schema.
type Mode string

const (
	// ModeStrict drops failing envelopes and returns an error to the
	// caller. The validator's logger receives a WARN line.
	ModeStrict Mode = "strict"
	// ModeWarn logs a WARN line and lets the envelope through.
	ModeWarn Mode = "warn"
	// ModeOff disables validation entirely. The hot path performs a
	// single nil check.
	ModeOff Mode = "off"
)

// Validator pairs a Registry with a Mode. Client libraries embed one in
// every Subscription so each received envelope is checked according to
// the operator's chosen policy.
type Validator struct {
	Registry Registry
	Mode     Mode
	Logger   *slog.Logger
}

// ErrPayloadInvalid is returned by Check in ModeStrict when the payload
// fails schema validation.
var ErrPayloadInvalid = errors.New("schema: payload does not conform")

// Check runs the validation policy against env. In ModeOff the call is
// a no-op. In ModeWarn validation errors are logged but the function
// returns nil. In ModeStrict a failure returns wrapped ErrPayloadInvalid.
//
// Envelopes whose channel has no registered pattern are accepted in all
// modes — the registry is intentionally an opt-in surface. Operators who
// want closed-world enforcement should wrap Check with a "no pattern =
// reject" rule outside this package.
func (v *Validator) Check(env envelope.Envelope) error {
	if v == nil || v.Mode == "" || v.Mode == ModeOff {
		return nil
	}
	if v.Registry == nil {
		return nil
	}
	p, _, err := v.Registry.Resolve(env.Channel)
	if err != nil {
		// Unknown channel — accept. Document the contract.
		return nil
	}
	a, ok := p.Aspects[env.Aspect]
	if !ok {
		if v.Mode == ModeStrict {
			return ErrUnsupportedAspect
		}
		v.logf("unsupported aspect", "channel", env.Channel, "aspect", env.Aspect)
		return nil
	}
	if a.PayloadSchema == nil {
		return nil
	}
	if err := a.PayloadSchema.ValidateBytes(env.Payload); err != nil {
		if v.Mode == ModeStrict {
			return errors.Join(ErrPayloadInvalid, err)
		}
		v.logf("payload invalid", "channel", env.Channel, "aspect", env.Aspect, "err", err.Error())
	}
	return nil
}

func (v *Validator) logf(msg string, args ...any) {
	if v.Logger == nil {
		slog.Default().Warn("schema: "+msg, args...)
		return
	}
	v.Logger.Warn("schema: "+msg, args...)
}
