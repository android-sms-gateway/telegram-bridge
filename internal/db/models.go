package db

import "time"

type TimedModel struct {
	CreatedAt time.Time `bun:",nullzero,notnull" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,notnull" json:"updated_at"`
}
