package canvas

import (
	"context"
	"testing"
)

func TestAnnouncementStatePollingWhileDispatchIntentChanges(t *testing.T) {
	c, _, calls := definedFixtureAt(t, actionDefinitionFixture(t), nil)
	p := acceptedAnnouncement(t, c, "# State polling concurrency\n")
	id, err := c.RequestAnnouncement(context.Background(), "announce", p.ID, "GM")
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.announcementOperation(id)
	if err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	failures := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			op.Stage = "pending"
			if i%2 == 0 {
				op.Stage = "uncertain"
			}
			if err := c.saveAnnouncement(op); err != nil {
				failures <- err
				return
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
		select {
		case err := <-failures:
			t.Error(err)
		default:
		}
	}()
	for range 25 {
		state, err := c.State()
		if err != nil {
			t.Fatal(err)
		}
		if len(state.Announcements) != 1 || state.Announcements[0].ID != id {
			t.Fatal("dispatch summaries not owned stable values")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("state polling invoked a model")
	}
}
