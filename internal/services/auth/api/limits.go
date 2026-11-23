package api

import "time"

// Limits are the limits on client calls. They are the same for every project and set for the
// instance (config auth.limits: the rates and bursts; the password backoff is fixed); the
// console shows them read-only.
type Limits struct {
	// Every client call, per address: a bucket of RequestBurst refilled at RequestsPerMinute.
	RequestsPerMinute, RequestBurst int
	// Account creation, per address.
	SignUpsPerMinute, SignUpBurst int
	// Wrong passwords per project and email within FailureWindow before the backoff, which
	// starts at BackoffBase and doubles up to BackoffMax.
	PasswordFailures                       int
	FailureWindow, BackoffBase, BackoffMax time.Duration
}

// ClientLimits are the default limits. config.Defaults holds the same rates and bursts.
var ClientLimits = Limits{
	RequestsPerMinute: 300, RequestBurst: 100,
	SignUpsPerMinute: 2, SignUpBurst: 100, // ≈ Firebase's 100 accounts per hour per address
	PasswordFailures: 10, FailureWindow: 15 * time.Minute, BackoffBase: 30 * time.Second, BackoffMax: 15 * time.Minute,
}
