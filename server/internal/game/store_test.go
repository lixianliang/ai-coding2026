package game

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreSeedsQuestions(t *testing.T) {
	store, err := OpenStore(sqliteInMemory)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	question, err := store.NextQuestion(context.Background(), 0)
	require.NoError(t, err)
	require.NotZero(t, question.ID)
	require.NotEmpty(t, question.Surface)
	require.NotEmpty(t, question.Answer)
}
