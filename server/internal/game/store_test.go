package game

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStoreSeedsQuestions(t *testing.T) {
	store, err := OpenStore(sqliteInMemory, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	question, err := store.NextQuestion(context.Background(), 0)
	require.NoError(t, err)
	require.NotZero(t, question.ID)
	require.NotEmpty(t, question.Surface)
	require.NotEmpty(t, question.Answer)
}

func TestStoreReplacesConfiguredQuestions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game.db")
	store, err := OpenStore(path, []Question{
		{ID: 1, Surface: "旧题", Answer: "旧答案"},
		{ID: 2, Surface: "保留题", Answer: "旧答案"},
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	store, err = OpenStore(path, []Question{
		{ID: 2, Surface: "更新后的题", Answer: "新答案"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	var questions []Question
	require.NoError(t, store.db.Order("id").Find(&questions).Error)
	require.Len(t, questions, 2)
	require.False(t, questions[0].Enabled)
	require.Equal(t, "更新后的题", questions[1].Surface)
	require.True(t, questions[1].Enabled)

	question, err := store.NextQuestion(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, uint(2), question.ID)
	require.Equal(t, "更新后的题", question.Surface)
}
