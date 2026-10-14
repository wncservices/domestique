package crew

import (
	"errors"
	"testing"
)

// Deleting a crew removes the crew and its membership together. Before, a
// failure on the second statement left a crew with no members behind a
// "deleted" error, or members of a crew that was gone.
func TestDeleteIsAllOrNothing(t *testing.T) {
	store := sqliteStore(t)
	ctx := t.Context()
	c, err := store.Create(ctx, "Sunday Club", "wilant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER keep_members BEFORE DELETE ON crew_members BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, c.ID); err == nil {
		t.Fatal("Delete swallowed a failure")
	}
	if _, err := store.Get(ctx, c.ID); err != nil {
		t.Errorf("the crew is gone although its delete failed: %v", err)
	}

	if _, err := store.db.Exec(`DROP TRIGGER keep_members`); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a second Delete = %v, want ErrNotFound", err)
	}
}

// A crew and its owner's membership are created together: a crew nobody owns
// is not something a failed create may leave behind.
func TestCreateIsAllOrNothing(t *testing.T) {
	store := sqliteStore(t)
	ctx := t.Context()
	if _, err := store.db.Exec(`CREATE TRIGGER no_members BEFORE INSERT ON crew_members BEGIN SELECT RAISE(ABORT, 'no'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, "Sunday Club", "wilant"); err == nil {
		t.Fatal("Create swallowed a failure")
	}
	if crews, _ := store.List(ctx); len(crews) != 0 {
		t.Errorf("a failed create left %d crew(s) with no owner", len(crews))
	}
}
