package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"gokin/internal/app"
	"gokin/internal/chat"
	"gokin/internal/pluginpolicy"
)

// One JSON value is one Write for both Encoder and streamJSONPresenter. Share
// this lock with control replies so progress can never split a response frame.
type headlessConnectionWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *headlessConnectionWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.Write(data)
}

type headlessControlRunner interface {
	HandleSDKControl(context.Context, json.RawMessage) (map[string]any, error)
}

type headlessInputFrame struct {
	data []byte
	err  error
}

type headlessControlJob struct {
	id      string
	request json.RawMessage
	ctx     context.Context
	cancel  context.CancelFunc
}

type headlessControlResult struct {
	id      string
	payload map[string]any
	err     error
}

func invokeHeadlessControl(runner headlessInputRunner, job headlessControlJob) (result headlessControlResult) {
	result.id = job.id
	defer func() {
		if recover() != nil {
			result.err = fmt.Errorf("control request handler panicked")
		}
	}()
	if err := job.ctx.Err(); err != nil {
		result.err = err
	} else if controlRunner, ok := runner.(headlessControlRunner); ok {
		result.payload, result.err = controlRunner.HandleSDKControl(job.ctx, job.request)
	} else {
		result.err = fmt.Errorf("SDK controls are unavailable for this runner")
	}
	return result
}

func writeHeadlessControl(writer io.Writer, id string, payload map[string]any, err error) error {
	response := map[string]any{"subtype": "success", "request_id": id, "response": payload}
	if err != nil {
		response = map[string]any{"subtype": "error", "request_id": id, "error": err.Error()}
	}
	return json.NewEncoder(writer).Encode(map[string]any{"type": "control_response", "response": response})
}

// User turns remain single-writer and FIFO. The reader keeps receiving control
// traffic while a turn runs; interrupt bypasses the serialized control worker.
// A closed input drains previously accepted turns and control requests.
func runHeadlessInputStream(ctx context.Context, runner headlessInputRunner, reader io.Reader, opts app.HeadlessOptions, sessionID string) error {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	fail := func(kind string, failure error) error {
		currentID := sessionID
		if sessionRunner, ok := runner.(interface{ GetSession() *chat.Session }); ok {
			if session := sessionRunner.GetSession(); session != nil {
				currentID = session.GetID()
			}
		}
		return errors.Join(failure, writeHeadlessFailure(opts.Stdout, currentID, kind, failure))
	}
	if runner == nil {
		return fail("app_init", fmt.Errorf("headless application is not initialized"))
	}
	if reader == nil {
		return fail("input", fmt.Errorf("--input-format stream-json requires redirected stdin"))
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	opts.Stdout = &headlessConnectionWriter{out: opts.Stdout}
	if opts.StreamState == nil {
		opts.StreamState = app.NewHeadlessStreamState()
	}
	frames := make(chan headlessInputFrame)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(frames)
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 64<<10), maxHeadlessStdinBytes+1)
		index := 0
		for scanner.Scan() {
			data := scanner.Bytes()
			if len(strings.TrimSpace(string(data))) == 0 {
				continue
			}
			index++
			frame := headlessInputFrame{data: append([]byte(nil), data...)}
			if len(data) > maxHeadlessStdinBytes {
				frame.err = fmt.Errorf("stream-json input record %d exceeds %d MiB limit", index, maxHeadlessStdinBytes>>20)
			} else if err := pluginpolicy.ValidateJSON(data); err != nil {
				frame.err = fmt.Errorf("invalid stream-json input record %d: %w", index, err)
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
			if frame.err != nil {
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case frames <- headlessInputFrame{err: fmt.Errorf("read stream-json input (each record must be at most %d MiB): %w", maxHeadlessStdinBytes>>20, err)}:
			case <-ctx.Done():
			}
		}
	}()
	defer func() {
		cancel()
		select {
		case <-readerDone:
		default:
			if closer, ok := reader.(io.Closer); ok {
				_ = closer.Close()
			}
		}
	}()

	controls := make(chan headlessControlJob, 32)
	controlResults := make(chan headlessControlResult, 1)
	workersDone := make(chan struct{})
	go func() {
		defer close(workersDone)
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-controls:
				result := invokeHeadlessControl(runner, job)
				job.cancel()
				select {
				case controlResults <- result:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	turnDone := make(chan error, 1)
	var turnCancel context.CancelFunc
	var queue []string
	queuedBytes, users, received := 0, 0, 0
	pending := map[string]context.CancelFunc{}
	pendingSizes := map[string]int{}
	pendingBytes := 0
	var inputErr error
	interrupted := false
	defer func() {
		cancel()
		for _, stop := range pending {
			stop()
		}
		if turnCancel != nil {
			turnCancel()
			<-turnDone // Teardown must precede CLI SessionEnd and session persistence.
		}
		<-workersDone
	}()
	for {
		if turnCancel == nil && len(queue) > 0 && len(pending) == 0 {
			prompt := queue[0]
			queue = queue[1:]
			queuedBytes -= len(prompt)
			turnCtx, stop := context.WithCancel(ctx)
			turnCancel, interrupted = stop, false
			go func() { _, err := runner.RunHeadlessWithOptions(turnCtx, prompt, opts); turnDone <- err }()
		}
		if frames == nil && turnCancel == nil && len(queue) == 0 && len(pending) == 0 {
			if inputErr != nil {
				return fail("input", inputErr)
			}
			if users == 0 && received == 0 {
				return fail("input", fmt.Errorf("--input-format stream-json received no user records"))
			}
			return nil
		}
		select {
		case <-ctx.Done():
			// Join an admitted turn before writing the connection failure, so its
			// terminal result cannot land after the connection's terminal envelope.
			if turnCancel != nil {
				turnCancel()
				<-turnDone
				turnCancel = nil
			}
			return fail("cancelled", ctx.Err())
		case result := <-controlResults:
			delete(pending, result.id)
			pendingBytes -= pendingSizes[result.id]
			delete(pendingSizes, result.id)
			if err := writeHeadlessControl(opts.Stdout, result.id, result.payload, result.err); err != nil {
				return err
			}
		case err := <-turnDone:
			turnCancel()
			turnCancel = nil
			if err != nil && !interrupted {
				return err
			}
		case frame, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			if frame.err != nil {
				inputErr, frames = frame.err, nil
				continue
			}
			received++
			var envelope struct {
				Type    string          `json:"type"`
				ID      string          `json:"request_id"`
				Request json.RawMessage `json:"request"`
			}
			_ = json.Unmarshal(frame.data, &envelope)
			envelope.Type = strings.ToLower(strings.TrimSpace(envelope.Type))
			switch envelope.Type {
			case "control_request":
				if envelope.ID == "" || len(envelope.ID) > 256 || strings.ContainsAny(envelope.ID, "\x00\n\r") {
					inputErr, frames = fmt.Errorf("control request requires a bounded request_id"), nil
					continue
				}
				if _, exists := pending[envelope.ID]; exists {
					inputErr, frames = fmt.Errorf("duplicate in-flight control request_id %q", envelope.ID), nil
					continue
				}
				var request struct {
					Subtype string `json:"subtype"`
				}
				_ = json.Unmarshal(envelope.Request, &request)
				if request.Subtype == "interrupt" {
					if turnCancel != nil {
						interrupted = true
						turnCancel()
					}
					if err := writeHeadlessControl(opts.Stdout, envelope.ID, map[string]any{}, nil); err != nil {
						return err
					}
					continue
				}
				if len(pending) >= 32 || pendingBytes+len(envelope.Request) > maxHeadlessStdinBytes {
					if err := writeHeadlessControl(opts.Stdout, envelope.ID, nil, fmt.Errorf("pending control requests exceed 32 records or 16 MiB")); err != nil {
						return err
					}
					continue
				}
				jobCtx, stop := context.WithCancel(ctx)
				pending[envelope.ID] = stop
				pendingSizes[envelope.ID] = len(envelope.Request)
				pendingBytes += len(envelope.Request)
				controls <- headlessControlJob{id: envelope.ID, request: envelope.Request, ctx: jobCtx, cancel: stop}
			case "control_cancel_request":
				if stop := pending[envelope.ID]; stop != nil {
					stop()
				}
			case "keep_alive", "control_response":
				// Unsolicited replies have no waiter and must be ignored.
			case "user":
				prompt, err := parseHeadlessStreamInput(frame.data)
				if err != nil {
					inputErr, frames = fmt.Errorf("invalid stream-json input record %d: %w", received, err), nil
					continue
				}
				if len(queue) >= 64 || queuedBytes+len(prompt) > maxHeadlessStdinBytes {
					inputErr, frames = fmt.Errorf("stream-json pending user input exceeds 64 records or 16 MiB"), nil
					continue
				}
				users++
				queue = append(queue, prompt)
				queuedBytes += len(prompt)
			default:
				inputErr, frames = fmt.Errorf("invalid stream-json input record %d: type must be %q or a control frame", received, "user"), nil
			}
		}
	}
}
