package metadata

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

func rollback(tx pgx.Tx) {
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tx.Rollback(c)
}
