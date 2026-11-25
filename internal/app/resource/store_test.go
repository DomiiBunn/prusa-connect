package resource_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/prusa3d/prusa-connect/internal/app/resource"
	"github.com/prusa3d/prusa-connect/internal/kernel/atomicfile"
	"github.com/prusa3d/prusa-connect/internal/kernel/ref"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpapi"
	"github.com/prusa3d/prusa-connect/internal/transport/http/httpfault"
	"github.com/prusa3d/prusa-connect/sdk/fault"
)

// TestTwoWritersQuotingOneVersionLeaveOneWinner is hazard 1 and an earlier design decision.
// Both goroutines read the same token; the compare-and-swap under the store's
// lock is what makes exactly one of them the writer.
func TestTwoWritersQuotingOneVersionLeaveOneWinner(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	version := h.version()

	const writers = 8

	var (
		start     sync.WaitGroup
		done      sync.WaitGroup
		won       atomic.Int64
		conflicts atomic.Int64
		other     atomic.Value
	)

	start.Add(1)
	done.Add(writers)

	for i := range writers {
		go func() {
			defer done.Done()

			start.Wait()

			_, applyErr := h.store.Apply(context.Background(), owner, instance,
				spec("http", fmt.Sprintf(`{"url":"https://%d.example"}`, i)),
				httpapi.Precondition{IfMatch: []string{version}})

			switch {
			case applyErr == nil:
				won.Add(1)
			case fault.HasCode(applyErr, httpfault.CodeVersionConflict()):
				conflicts.Add(1)
			default:
				other.Store(applyErr.Error())
			}
		}()
	}

	start.Done()
	done.Wait()

	require.Nil(t, other.Load(), "every refusal must be a version conflict")
	require.Equal(t, int64(1), won.Load(), "exactly one writer may spend one token")
	require.Equal(t, int64(writers-1), conflicts.Load())
	require.True(t, parses(h.overlayBytes()))
	require.Empty(t, h.stray())
}

// TestAReaderRacingWritesNeverSeesAHalfWrittenFile is an earlier design decision's reason for
// existing: a half-written configuration is a server that will not start.
func TestAReaderRacingWritesNeverSeesAHalfWrittenFile(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" {
		// Go opens a file for reading without FILE_SHARE_DELETE, so MoveFileEx
		// refuses to replace a destination another handle holds. The rename is
		// still all-or-nothing; what Windows does not offer is replacing a file
		// somebody is reading, which is what this proof needs.
		t.Skip("a concurrent reader blocks the rename on Windows")
	}

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	stop := make(chan struct{})
	torn := make(chan []byte, 1)

	var reader sync.WaitGroup

	reader.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}

			content, readErr := os.ReadFile(h.paths.Overlay)
			if readErr != nil {
				continue
			}

			if !parses(content) {
				select {
				case torn <- content:
				default:
				}

				return
			}
		}
	})

	const writes = 60

	for i := range writes {
		_, applyErr := h.update(instance, spec("http", fmt.Sprintf(`{"url":"https://%d.example"}`, i)), h.version())
		require.NoError(t, applyErr)
	}

	close(stop)
	reader.Wait()

	select {
	case content := <-torn:
		t.Fatalf("a reader saw a partial overlay after %d atomic writes:\n%s", writes, content)
	default:
	}
}

// TestAHandEditBetweenTheReadAndTheWriteIsRefused is hazard 1 in its plainest
// form: the API read a version, a person saved the file, and the API write must
// not discard what they typed.
func TestAHandEditBetweenTheReadAndTheWriteIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	version := h.version()

	handEdited := `{
  "version": 1,
  "identities": {"sink/webhook": {"uid": "01K0000000000000000000000A", "generation": 9, "digest": "typed"}},
  "instances": [
    {"kind": "sink", "name": "webhook", "type": "http", "enabled": false, "required": false,
     "config": {"url": "https://typed-by-a-person.example"}}
  ]
}
`
	h.writeOverlayByHand(handEdited)

	_, err = h.update(instance, spec("http", `{"url":"https://api.example"}`), version)
	require.Error(t, err)
	require.True(t, fault.HasCode(err, httpfault.CodeVersionConflict()), "want a version conflict, got %v", err)

	require.Equal(t, handEdited, string(h.overlayBytes()), "the hand edit must survive the refused write")

	found, err := h.get(instance)
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"https://typed-by-a-person.example"}`, string(found.Config))
	require.False(t, found.Enabled)
}

// TestAnEditWhileTheWriteIsStagedIsRefused reaches the second half of the
// compare-and-swap: the re-read immediately before the rename. The validator is
// the seam because it is the last thing called before the file is staged, so an
// edit made from it lands in the window nothing else can reach.
func TestAnEditWhileTheWriteIsStagedIsRefused(t *testing.T) {
	t.Parallel()

	var h *harness

	edit := &editingValidator{}

	h = newHarness(t, func(cfg *resource.Config) { cfg.Validator = edit })
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	version := h.version()
	typed := `{"version":1,"instances":[]}` + "\n"

	edit.once = func() { h.writeOverlayByHand(typed) }

	_, err = h.update(instance, spec("http", `{"url":"https://api.example"}`), version)
	require.Error(t, err)
	require.True(t, fault.HasCode(err, httpfault.CodeVersionConflict()), "want a version conflict, got %v", err)

	require.Equal(t, typed, string(h.overlayBytes()))
	require.Empty(t, h.stray(), "a refused write must discard what it staged")
}

// editingValidator edits the configuration from inside the write it is
// validating, which is the only way to be certain the guard between staging and
// renaming is the one that fires.
type editingValidator struct {
	once func()
	done bool
}

func (v *editingValidator) Validate(context.Context, ref.Ref, string, json.RawMessage) error {
	if v.once != nil && !v.done {
		v.done = true

		v.once()
	}

	return nil
}

// TestACrashBeforeTheRenameLeavesALoadableConfiguration is an earlier design decision seen
// from the loader: whatever a crash leaves in the directory, the previous
// configuration is still what loads.
func TestACrashBeforeTheRenameLeavesALoadableConfiguration(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	before := h.overlayBytes()

	// A crash between the write and the rename is a staged file that was never
	// committed.
	staged, err := atomicfile.Stage(h.paths.Overlay, []byte("half a document"), 0o600)
	require.NoError(t, err)
	require.NotEmpty(t, staged.Path())

	require.Equal(t, string(before), string(h.overlayBytes()))

	doc, err := resource.Load(h.paths)
	require.NoError(t, err)
	require.Len(t, doc.Instances, 1)
	require.Equal(t, "http", doc.Instances[0].Spec.Type)

	found, err := h.get(instance)
	require.NoError(t, err)
	require.JSONEq(t, `{"url":"https://a.example"}`, string(found.Config))
}

// TestARejectedInstanceLeavesTheFileByteIdentical is an earlier design decision. Every refusal
// the store can make is exercised, because "validated before the write" is only
// true if it holds for all of them.
func TestARejectedInstanceLeavesTheFileByteIdentical(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(cfg *resource.Config) { cfg.Validator = refusingValidator{} })
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("ok", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	before := h.overlayBytes()
	version := h.version()

	fresh := mustRef(t, ref.KindSink, "not-there-yet")

	cases := []struct {
		name     string
		instance ref.Ref
		spec     httpapi.ResourceSpec
		pre      httpapi.Precondition
		code     fault.Code
	}{
		{
			name:     "the implementation refuses the settings",
			instance: fresh,
			spec:     spec("refuse", `{"url":"https://b.example"}`),
			pre:      httpapi.Precondition{MustNotExist: true},
			code:     httpfault.CodeRequestInvalid(),
		},
		{
			name:     "the type is empty",
			instance: fresh,
			spec:     spec("", `{"url":"https://b.example"}`),
			pre:      httpapi.Precondition{MustNotExist: true},
			code:     httpfault.CodeRequestInvalid(),
		},
		{
			name:     "the settings block is not an object",
			instance: instance,
			spec:     spec("ok", `["not","an","object"]`),
			pre:      httpapi.Precondition{IfMatch: []string{version}},
			code:     httpfault.CodeRequestInvalid(),
		},
		{
			name:     "the version is stale",
			instance: instance,
			spec:     spec("ok", `{"url":"https://b.example"}`),
			pre:      httpapi.Precondition{IfMatch: []string{"not-the-current-version"}},
			code:     httpfault.CodeVersionConflict(),
		},
		{
			name:     "the type changed",
			instance: instance,
			spec:     spec("other", `{"url":"https://a.example"}`),
			pre:      httpapi.Precondition{IfMatch: []string{version}},
			code:     httpfault.CodeFieldImmutable(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Every case here is a refusal, so nothing they share can move
			// underneath them and they run together.
			t.Parallel()

			_, applyErr := h.store.Apply(t.Context(), owner, tc.instance, tc.spec, tc.pre)
			require.Error(t, applyErr)
			require.True(t, fault.HasCode(applyErr, tc.code), "want %s, got %v", tc.code, applyErr)
			require.Equal(t, string(before), string(h.overlayBytes()), "a refused write must not touch the file")
			require.Empty(t, h.stray())
		})
	}
}

var errNoUseForTheseSettings = errors.New("this implementation has no use for these settings")

type refusingValidator struct{}

func (refusingValidator) Validate(_ context.Context, _ ref.Ref, typeName string, _ json.RawMessage) error {
	if typeName == "refuse" {
		return errNoUseForTheseSettings
	}

	return nil
}

// TestChangingTypeIsRefusedRatherThanReplacing is an earlier design decision. A different
// implementation cannot inherit an instance's identity, and the rows keyed by
// its UID would follow the wrong type.
func TestChangingTypeIsRefusedRatherThanReplacing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindStorage, "local")

	created, err := h.create(instance, spec("filesystem", `{"root":"/var/lib/prusa"}`))
	require.NoError(t, err)
	require.True(t, created.Created)

	before := h.overlayBytes()
	h.plane.reset()

	_, err = h.update(instance, spec("s3", `{"root":"/var/lib/prusa"}`), h.version())
	require.Error(t, err)
	require.True(t, fault.HasCode(err, httpfault.CodeFieldImmutable()), "want an immutable field, got %v", err)

	var carried *fault.Fault

	require.ErrorAs(t, err, &carried)
	require.Contains(t, carried.Error(), "fixed for the life of the instance")

	require.Equal(t, string(before), string(h.overlayBytes()))
	require.Empty(t, h.plane.outcomes(), "a refused write must not reach the supervision plane")

	found, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, "filesystem", found.Type)
	require.Equal(t, created.Instance.UID, found.UID, "the identity must not have moved")
}

// TestReloadingAnUnchangedConfigurationIsANoOp is an earlier design decision's first
// requirement: reconcile at startup and reconcile after a write are one path,
// and running it against bytes that did not change must not restart anything.
func TestReloadingAnUnchangedConfigurationIsANoOp(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeFile(`
resources:
  storage:
    local:
      type: filesystem
      config:
        root: /var/lib/prusa
`)

	require.NoError(t, h.store.Reload(t.Context()))
	require.Equal(t, []string{"start storage:local"}, h.plane.outcomes())

	first := h.overlayBytes()
	require.NotEmpty(t, first, "the first load records the identity it minted")

	h.plane.reset()

	for range 3 {
		require.NoError(t, h.store.Reload(t.Context()))
	}

	require.Empty(t, h.plane.outcomes(), "an unchanged configuration must produce no steps")
	require.Zero(t, h.plane.planCount(), "an empty plan must not reach the plane at all")
	require.Equal(t, string(first), string(h.overlayBytes()), "an unchanged load must not write")

	found, err := h.get(mustRef(t, ref.KindStorage, "local"))
	require.NoError(t, err)
	require.Equal(t, int64(1), found.Generation, "reloading is not an accepted change")
	require.Equal(t, found.Generation, found.Status.ObservedGeneration)
}

// TestTheOutcomeMatchesWhatChanged separates an earlier design decision's three outcomes. They
// are three because conflating them is how an operator loses an instance to an
// edit that should have been applied in place.
func TestTheOutcomeMatchesWhatChanged(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)
	require.Equal(t, []string{"start sink:webhook"}, h.plane.outcomes())

	h.plane.reset()

	_, err = h.update(instance, spec("http", `{"url":"https://b.example"}`), h.version())
	require.NoError(t, err)
	require.Equal(t, []string{"restart sink:webhook"}, h.plane.outcomes(), "a settings change is a restart")

	h.plane.reset()

	required := spec("http", `{"url":"https://b.example"}`)
	required.Required = true

	_, err = h.update(instance, required, h.version())
	require.NoError(t, err)
	require.Equal(t, []string{"in-place sink:webhook"}, h.plane.outcomes(), "required is host policy")

	h.plane.reset()

	off := required
	off.Enabled = false

	_, err = h.update(instance, off, h.version())
	require.NoError(t, err)
	require.Equal(t, []string{"stop sink:webhook"}, h.plane.outcomes())

	h.plane.reset()

	require.NoError(t, h.store.Delete(t.Context(), owner, instance,
		httpapi.Precondition{IfMatch: []string{h.version()}}))
	require.Equal(t, []string{"stop sink:webhook"}, h.plane.outcomes(),
		"a removed instance reaches the plane even when stopped, so it drops what it kept for it")
}

// TestReorderingTheSettingsIsNotAChange is what makes the restart outcome
// trustworthy. An editor that sorts keys, or a client that echoes a read back,
// must not restart the instance.
func TestReorderingTheSettingsIsNotAChange(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example","retries":3}`))
	require.NoError(t, err)

	h.plane.reset()

	_, err = h.update(instance, spec("http", `{ "retries" : 3 , "url" : "https://a.example" }`), h.version())
	require.NoError(t, err)
	require.Empty(t, h.plane.outcomes(), "the same settings written differently is not a change")
}

// TestReadingAnInstanceWithNoSettingsAndWritingItBackIsNotAChange is the round
// trip a page makes: read the row, flip enabled, put the same body back. A read
// of an instance that declares no settings answers an empty object, because the
// member is published as a required object, so the empty object has to mean
// what its absence means. Storing it as settings instead would make the client's
// own row, written back untouched, a change that moves the generation.
func TestReadingAnInstanceWithNoSettingsAndWritingItBackIsNotAChange(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", ""))
	require.NoError(t, err)

	read, err := h.get(instance)
	require.NoError(t, err)

	body, err := json.Marshal(read.Config)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(body), "a member published as an object may not be read back as null")

	h.plane.reset()

	back := read.ResourceSpec
	back.Enabled = !back.Enabled

	written, err := h.update(instance, back, h.version())
	require.NoError(t, err)
	require.Empty(t, written.Instance.Config, "an empty settings object is the absence it was read from")

	again, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, written.Instance.Generation, again.Generation)
}

// TestAnInstanceTheOperatorDeclaredIsNotWritable is earlier research, and
// the precedence this store chose: the hand-authored file wins, and the refusal
// names it.
func TestAnInstanceTheOperatorDeclaredIsNotWritable(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeFile(`
resources:
  storage:
    local:
      type: filesystem
      config:
        root: /var/lib/prusa
`)
	require.NoError(t, h.store.Reload(t.Context()))

	instance := mustRef(t, ref.KindStorage, "local")

	found, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, "file", found.Source)

	before := h.overlayBytes()

	_, err = h.update(instance, spec("filesystem", `{"root":"/tmp"}`), h.version())
	require.Error(t, err)
	require.True(t, fault.HasCode(err, httpfault.CodeResourceNotWritable()), "got %v", err)

	var carried *fault.Fault

	require.ErrorAs(t, err, &carried)
	require.Equal(t, string(before), string(h.overlayBytes()))

	err = h.store.Delete(t.Context(), owner, instance,
		httpapi.Precondition{IfMatch: []string{h.version()}})
	require.Error(t, err)
	require.True(t, fault.HasCode(err, httpfault.CodeResourceNotWritable()), "got %v", err)
}

// TestTheFileShadowsAnOverlayInstanceOfTheSameName is the precedence rule seen
// from the other side, and the failure Grafana warns about: an instance created
// through the API stops being the live one the moment the operator declares the
// same name by hand. It is reported rather than silent.
func TestTheFileShadowsAnOverlayInstanceOfTheSameName(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindStorage, "local")

	_, err := h.create(instance, spec("filesystem", `{"root":"/from-the-api"}`))
	require.NoError(t, err)

	h.writeFile(`
resources:
  storage:
    local:
      type: filesystem
      config:
        root: /from-the-file
`)

	doc, err := resource.Load(h.paths)
	require.NoError(t, err)
	require.Equal(t, []ref.Ref{instance}, doc.Shadowed)

	found, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, "file", found.Source)
	require.JSONEq(t, `{"root":"/from-the-file"}`, string(found.Config))

	// The overlay's bytes are still there, so removing the block from the file
	// gives the operator their instance back rather than nothing.
	require.Contains(t, string(h.overlayBytes()), "/from-the-api")
}

// TestCreatingRequiresAPreconditionThatSaysSo keeps the transport's contract:
// an If-Match against an instance that is not there is a 404, not a create.
func TestCreatingRequiresAPreconditionThatSaysSo(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.update(instance, spec("http", `{"url":"https://a.example"}`), "whatever")
	require.Error(t, err)
	require.True(t, fault.HasCode(err, httpfault.CodeResourceNotFound()), "got %v", err)
	require.Empty(t, h.overlayBytes())
}

// TestADryRunWritesNothing is what the transport promises when it strips the
// ETag from the answer: nothing was written, so there is no token to quote.
func TestADryRunWritesNothing(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	result, err := h.store.Apply(t.Context(), owner, instance, spec("http", `{"url":"https://a.example"}`),
		httpapi.Precondition{MustNotExist: true, DryRun: true})
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Empty(t, h.overlayBytes())
	require.Empty(t, h.plane.outcomes())
	require.Empty(t, h.stray())
}

// TestDeleteTakesTheIdentityWithIt is earlier research: remove and add is two
// instances, not one renamed, or the rows that referenced the first would
// follow the second.
func TestDeleteTakesTheIdentityWithIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	created, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	require.NoError(t, h.store.Delete(t.Context(), owner, instance,
		httpapi.Precondition{IfMatch: []string{h.version()}}))

	recreated, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)
	require.NotEqual(t, created.Instance.UID, recreated.Instance.UID)
	require.Equal(t, int64(1), recreated.Instance.Generation)
}

// TestTheOverlayKeepsThePreviousVersion is an earlier design decision's sixth step: the copy an
// operator restores from when a write turns out to have been wrong.
func TestTheOverlayKeepsThePreviousVersion(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	_, err := h.create(instance, spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	first := h.overlayBytes()

	_, err = h.update(instance, spec("http", `{"url":"https://b.example"}`), h.version())
	require.NoError(t, err)

	backup, err := os.ReadFile(h.paths.Overlay + ".bak")
	require.NoError(t, err)
	require.Equal(t, string(first), string(backup))
	require.Contains(t, string(h.overlayBytes()), "https://b.example")
}

// TestUnknownKeysInTheOperatorsFileFailThatInstance is earlier research
// recommendation 8. A typo that is silently ignored is a setting an operator
// believes is in force, so the block is refused; it is refused as one instance
// and not as the whole file, which is earlier research.
func TestUnknownKeysInTheOperatorsFileFailThatInstance(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeFile(`
resources:
  storage:
    local:
      type: filesystem
      entpoint: http://localhost
  sink:
    audit:
      type: webhook
`)

	doc, err := resource.Load(h.paths)
	require.NoError(t, err)

	require.Len(t, doc.Instances, 1)
	require.Equal(t, "audit", doc.Instances[0].Ref.ID(),
		"the instance that loads was dropped because another one did not")

	require.Len(t, doc.Failures, 1)
	require.Equal(t, "resources.storage.local", doc.Failures[0].Key)
	require.ErrorIs(t, doc.Failures[0].Err, resource.ErrConfigInvalid)
	require.Contains(t, doc.Failures[0].Err.Error(), "entpoint")
}

// TestTheTokenCoversBothInputs is earlier research: a counter the server
// increments on its own writes cannot see a hand edit, and only a hash over the
// bytes as loaded can.
func TestTheTokenCoversBothInputs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	// One instance, because the token is read off a listing and a listing with
	// nothing in it carries none. That is TestAnEmptyListingCarriesNoVersion.
	_, err := h.create(mustRef(t, ref.KindSink, "webhook"), spec("http", `{"url":"https://a.example"}`))
	require.NoError(t, err)

	created := h.version()
	require.NotEmpty(t, created)

	h.writeFile("resources: {}\n")
	require.NotEqual(t, created, h.version(), "an edit to the operator's file must move the token")

	withFile := h.version()

	// The same instances and one more byte, so what moved is the file and not
	// what it declares.
	h.writeOverlayByHand(string(h.overlayBytes()) + "\n")
	require.NotEqual(t, withFile, h.version(), "an edit to the overlay must move the token")
}

// TestListNarrowsToOneKind keeps the filter honest: it is the query, not a
// filter applied to rows that were already produced for someone else.
func TestListNarrowsToOneKind(t *testing.T) {
	t.Parallel()

	h := newHarness(t)

	_, err := h.create(mustRef(t, ref.KindSink, "webhook"), spec("http", `{}`))
	require.NoError(t, err)

	_, err = h.create(mustRef(t, ref.KindStorage, "local"), spec("filesystem", `{}`))
	require.NoError(t, err)

	all, err := h.store.List(t.Context(), owner, "")
	require.NoError(t, err)
	require.Len(t, all.Items, 2)
	require.Equal(t, "storage", all.Items[0].Kind, "kinds list in the closed set's order")

	sinks, err := h.store.List(t.Context(), owner, ref.KindSink)
	require.NoError(t, err)
	require.Len(t, sinks.Items, 1)
	require.Equal(t, "webhook", sinks.Items[0].Name)
	require.Equal(t, all.Version, sinks.Items[0].Version, "the token covers the file, not the row")
}

// TestAnOverlayFromANewerBuildIsRefused stops this binary reading a file it
// only half understands and writing back what it did not read.
func TestAnOverlayFromANewerBuildIsRefused(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeOverlayByHand(`{"version":99,"instances":[]}`)

	_, err := resource.Load(h.paths)
	require.ErrorIs(t, err, resource.ErrConfigInvalid)
	require.Contains(t, err.Error(), "format 99")
}

func TestNewRefusesAStoreWithNoOverlay(t *testing.T) {
	t.Parallel()

	_, err := resource.New(resource.Config{Paths: resource.Paths{File: filepath.Join(t.TempDir(), "config.yaml")}})
	require.ErrorIs(t, err, resource.ErrConfigInvalid)
}

// TestReconcileFailureDoesNotFailAnAcceptedWrite records the choice: the
// declaration is on disk, so the write succeeded. A supervision failure is
// reported on the status axis, which is the axis for it.
func TestReconcileFailureDoesNotFailAnAcceptedWrite(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.plane.failWith = errStartFailed

	_, err := h.create(mustRef(t, ref.KindSink, "webhook"), spec("http", `{}`))
	require.NoError(t, err, "an accepted declaration is written whether or not it starts")
	require.Contains(t, string(h.overlayBytes()), "webhook")

	// Startup is the other half of the same choice, and it is the same answer:
	// see TestAPlaneRefusalAtStartupFailsTheInstanceAndNotTheProcess.
}

// TestAPlaneRefusalAtStartupFailsTheInstanceAndNotTheProcess is earlier research
// §10.4 rule 1 on the boot path: "rejection is per instance, never
// process-wide". A plane that will not take one instance leaves the document
// adopted, the instance declared, and the process able to serve. Whether it is
// then ready is an earlier design decision's question and readiness answers it.
//
// The refusal is not silent. The operator's only feedback that their block is
// not running is the log line, so the line names the instance the plane
// refused, one line per instance, on the same shape as a load failure.
func TestAPlaneRefusalAtStartupFailsTheInstanceAndNotTheProcess(t *testing.T) {
	t.Parallel()

	logged := &bytes.Buffer{}
	boot := newHarness(t, func(cfg *resource.Config) {
		cfg.Logger = slog.New(slog.NewJSONHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	boot.plane.failWith = errors.Join(
		fmt.Errorf("start sink:webhook: %w", errStartFailed),
		fmt.Errorf("start storage:local: %w", errStartFailed),
	)
	boot.writeFile("resources:\n  sink:\n    webhook:\n      type: http\n")

	require.NoError(t, boot.store.Reload(t.Context()),
		"one instance the plane refused must not stop the process")

	require.Len(t, boot.store.Document().Instances, 1,
		"the declaration is adopted whether or not the plane took it")

	lines := strings.Count(logged.String(), "the supervision plane refused")
	require.Equal(t, 2, lines, "one line per refused instance, not one joined line: %s", logged.String())
	require.Contains(t, logged.String(), "sink:webhook")
	require.Contains(t, logged.String(), "storage:local")
}

var errStartFailed = errors.New("the endpoint refused the connection")

func TestDocumentAndVersionReportTheLastReconciledState(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	require.Empty(t, h.store.Document().Instances)

	_, err := h.create(mustRef(t, ref.KindSink, "webhook"), spec("http", `{}`))
	require.NoError(t, err)

	doc := h.store.Document()
	require.Len(t, doc.Instances, 1)
	require.Equal(t, doc.Version, h.store.Version())
	require.Equal(t, resource.SourceOverlay, doc.Instances[0].Source)
}

// TestAListingSaysWhichInstanceIsAModuleAndWhereItsDocumentIs is what lets a
// client discover enabled modules in one call: the runtime, the namespace the
// type produces, and the document address once the plane holds a document
// behind it.
func TestAListingSaysWhichInstanceIsAModuleAndWhereItsDocumentIs(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeFile(`
resources:
  storage:
    local:
      type: filesystem
      config:
        root: /var/lib/prusa
  sink:
    planner:
      type: com.github.aux.acme.planner/planner
      runtime: service
      service:
        path: /opt/pcl/plugins
        checksum: sha256:` + strings.Repeat("a", 64) + `
        contract_major: 1
`)

	require.NoError(t, h.store.Reload(t.Context()))

	planner := mustRef(t, ref.KindSink, "planner")

	before, err := h.get(planner)
	require.NoError(t, err)
	require.Equal(t, "service", before.Runtime)
	require.Equal(t, "com.github.aux.acme.planner", before.Namespace)
	require.Empty(t, before.OpenAPIURL, "the plane holds no document, and the listing published an address")

	builtin, err := h.get(mustRef(t, ref.KindStorage, "local"))
	require.NoError(t, err)
	require.Equal(t, "builtin", builtin.Runtime, "an instance with no runtime key reads as builtin")
	require.Empty(t, builtin.Namespace)
	require.Empty(t, builtin.OpenAPIURL)

	h.plane.mu.Lock()
	h.plane.documented = map[ref.Ref]bool{planner: true}
	h.plane.mu.Unlock()

	after, err := h.get(planner)
	require.NoError(t, err)
	require.Equal(t, "/api/v1/modules/com.github.aux.acme.planner/openapi.json", after.OpenAPIURL)
}

// TestTheListingPublishesTheUIBlockOfAReadyModuleOnly is ADR-20260911-4p6 rule 32.
// The block reaches the listing for an enabled module the plane holds a bundle
// for, and for nothing else: not for the same instance switched off, and not
// for a module that runs a process and draws nothing.
func TestTheListingPublishesTheUIBlockOfAReadyModuleOnly(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeFile(`
resources:
  asset:
    spools:
      type: com.github.aux.acme.spools/spools
      asset:
        path: /opt/pcl/plugins/spools/ui
        checksum: sha256:` + strings.Repeat("b", 64) + `
  sink:
    planner:
      type: com.github.aux.acme.planner/planner
      runtime: service
      service:
        path: /opt/pcl/plugins
        checksum: sha256:` + strings.Repeat("a", 64) + `
        contract_major: 1
`)

	require.NoError(t, h.store.Reload(t.Context()))

	spools := mustRef(t, ref.KindAsset, "spools")
	planner := mustRef(t, ref.KindSink, "planner")

	drawn := httpapi.ResourceUI{
		EntryURL: "/api/v1/modules/com.github.aux.acme.spools/ui/index.js",
		Library:  ">=0.3.0 <1.0.0",
		Contributes: httpapi.ResourceContributes{
			Pages: []httpapi.ResourcePage{{Path: "spools", Title: "Spools"}},
		},
	}

	h.plane.mu.Lock()
	h.plane.drawn = map[ref.Ref]httpapi.ResourceUI{spools: drawn}
	h.plane.mu.Unlock()

	ready, err := h.get(spools)
	require.NoError(t, err)
	require.NotNil(t, ready.UI, "a ready module with a ui block published none")
	require.Equal(t, drawn, *ready.UI)

	serving, err := h.get(planner)
	require.NoError(t, err)
	require.Nil(t, serving.UI, "a module that runs a process and draws nothing published a ui block")

	// The same instance switched off. The plane still answers, because the
	// bundle is not torn down the instant the file changes, and the listing
	// has to publish nothing for it anyway.
	h.writeFile(`
resources:
  asset:
    spools:
      type: com.github.aux.acme.spools/spools
      enabled: false
      asset:
        path: /opt/pcl/plugins/spools/ui
        checksum: sha256:` + strings.Repeat("b", 64) + `
`)

	require.NoError(t, h.store.Reload(t.Context()))

	disabled, err := h.get(spools)
	require.NoError(t, err)
	require.False(t, disabled.Enabled)
	require.Nil(t, disabled.UI, "a disabled instance published an entry address a page would import")
}

// TestTheEnabledSwitchReachesThePlaneOnTheWriteThatMovedIt is the other half of
// an install taking effect while the server runs: a module installed switched
// off is switched on through put-resource, and that write starts it here rather
// than at the next start of this process.
//
// The switch is a write like any other, so it travels the route every write
// travels: Apply, commit, reconcile. The plan is what proves it, because a
// reconcile that produced no step is a write that changed the file and nothing
// else.
func TestTheEnabledSwitchReachesThePlaneOnTheWriteThatMovedIt(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	off := spec("http", `{"url":"https://a.example"}`)
	off.Enabled = false

	created, err := h.create(instance, off)
	require.NoError(t, err)
	require.True(t, created.Created)
	require.Empty(t, h.plane.outcomes(), "an instance declared switched off was started")

	on := off
	on.Enabled = true

	started, err := h.update(instance, on, h.version())
	require.NoError(t, err)
	require.True(t, started.Instance.Enabled)
	require.Equal(t, []string{"start " + instance.String()}, h.plane.outcomes())

	// And back, so the pair is tested in both directions.
	_, err = h.update(instance, off, h.version())
	require.NoError(t, err)
	require.Equal(t,
		[]string{"start " + instance.String(), "stop " + instance.String()},
		h.plane.outcomes())
}

// errDirectoryRefused is the graph refusing the one write a reconcile makes to
// it, which is what an unreachable database looks like from here.
var errDirectoryRefused = errors.New("the instance directory could not be written")

// refusingDirectory is the graph with that one write failing and every read of
// it working, so a case reaches the reconcile rather than the authorization
// check in front of it.
type refusingDirectory struct {
	resource.Graph
}

func (refusingDirectory) SyncResources(context.Context, ...ref.Ref) error {
	return errDirectoryRefused
}

// TestAReconcileTheDirectoryRefusedStillAdoptsTheDocument holds memory to what
// is on disk. The identity of a new instance is written to the overlay before
// the reconcile runs, so a reconcile that gave up on the directory left the
// store diffing against a document older than the file beside it: the plane was
// never told to start the instance, and the next reload produced the same plan
// again.
func TestAReconcileTheDirectoryRefusedStillAdoptsTheDocument(t *testing.T) {
	t.Parallel()

	h := newHarness(t, func(cfg *resource.Config) {
		cfg.Graph = refusingDirectory{Graph: cfg.Graph}
	})

	h.writeFile(`
resources:
  storage:
    local:
      type: filesystem
      config:
        root: /var/lib/prusa
`)

	require.ErrorIs(t, h.store.Reload(t.Context()), errDirectoryRefused,
		"the caller was not told the directory refused the declared set")
	require.Equal(t, []string{"start storage:local"}, h.plane.outcomes(),
		"the instance is on disk and the plane was never told to run it")

	require.ErrorIs(t, h.store.Reload(t.Context()), errDirectoryRefused)
	require.Equal(t, []string{"start storage:local"}, h.plane.outcomes(),
		"the store diffed against a document older than the file on disk")
}

// TestHiddenContributionsSurviveAWriteAndMoveNothingTheHostRuns holds the list
// as a presentation key: it reads back, and writing it neither restarts the
// instance nor counts as an accepted change.
func TestHiddenContributionsSurviveAWriteAndMoveNothingTheHostRuns(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	created, err := h.create(instance, spec("http", ""))
	require.NoError(t, err)

	h.plane.reset()

	hide := created.Instance.ResourceSpec
	hide.HiddenContributions = []string{"device_card/fleet-stats", "pages/fleet-stats"}

	written, err := h.update(instance, hide, h.version())
	require.NoError(t, err)
	require.Empty(t, h.plane.outcomes())
	require.Equal(t, created.Instance.Generation, written.Instance.Generation)

	again, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, hide.HiddenContributions, again.HiddenContributions)
}

// A hidden settings tab or Printer tab card is dropped, on a write and on a
// read, so the module's page still saves and the entry hides nothing.
func TestAnEntryThatCanNoLongerBeHiddenIsDropped(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	created, err := h.create(instance, spec("http", ""))
	require.NoError(t, err)

	hide := created.Instance.ResourceSpec
	hide.HiddenContributions = []string{"settings/how-to", "device_info_section/card", "device_card/kept"}

	_, err = h.update(instance, hide, h.version())
	require.NoError(t, err)

	again, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, []string{"device_card/kept"}, again.HiddenContributions)
}

// An entry written into config.yaml by hand, which no write checks, is dropped
// when the instance is read, so it hides nothing.
func TestAHandWrittenEntryThatCanNoLongerBeHiddenIsDroppedOnRead(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	h.writeFile(`resources:
  sink:
    audit:
      type: webhook
      hidden_contributions:
        - settings/how-to
        - device_info_section/card
        - device_card/kept
`)
	require.NoError(t, h.store.Reload(t.Context()))

	got, err := h.get(mustRef(t, ref.KindSink, "audit"))
	require.NoError(t, err)
	require.Equal(t, []string{"device_card/kept"}, got.HiddenContributions)
}

// TestEveryHideableSlotIsAccepted writes one entry for each slot an operator
// may hide, the file menu and the print dialog among them.
func TestEveryHideableSlotIsAccepted(t *testing.T) {
	t.Parallel()

	h := newHarness(t)
	instance := mustRef(t, ref.KindSink, "webhook")

	created, err := h.create(instance, spec("http", ""))
	require.NoError(t, err)

	hide := created.Instance.ResourceSpec
	hide.HiddenContributions = []string{
		"pages/plan", "device_card/plan", "device_section/plan",
		"device_tab/plan", "device_action/plan", "file_action/plan", "print_section/planner",
	}

	_, err = h.update(instance, hide, h.version())
	require.NoError(t, err)

	again, err := h.get(instance)
	require.NoError(t, err)
	require.Equal(t, hide.HiddenContributions, again.HiddenContributions)
}

func TestAMalformedHiddenContributionIsRefused(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, 65)
	for i := range tooMany {
		tooMany[i] = "device_card/" + strconv.Itoa(i)
	}

	for name, hidden := range map[string][]string{
		"an unknown slot":   {"menu/x"},
		"a dashboard card":  {"cards/x"},
		"no identity":       {"pages/"},
		"whitespace":        {"pages/a b"},
		"a duplicate":       {"pages/a", "pages/a"},
		"too long":          {"pages/" + strings.Repeat("a", 200)},
		"more than allowed": tooMany,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			h := newHarness(t)
			instance := mustRef(t, ref.KindSink, "webhook")

			created, err := h.create(instance, spec("http", ""))
			require.NoError(t, err)

			bad := created.Instance.ResourceSpec
			bad.HiddenContributions = hidden

			_, err = h.update(instance, bad, h.version())
			require.Error(t, err)
		})
	}
}
