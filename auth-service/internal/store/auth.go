package store

import "context"

type AuthData struct {
}

type AuthStore interface {
	SetState(ctx context.Context, state string) error
	GetState(
		ctx context.Context,
		state string,
	) (string, error)
	DeleteState(
		ctx context.Context,
		state string,
	) error
}
