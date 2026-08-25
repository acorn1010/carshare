package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"carshare/internal/store"
)

// maxBodyBytes caps request bodies. No endpoint needs more than a small JSON
// object.
const maxBodyBytes = 64 * 1024

// errorBody is the uniform error envelope: {"error":{"code","message"}}.
type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	var body errorBody
	body.Error.Code = code
	body.Error.Message = message
	writeJSON(writer, status, body)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// decodeJSON reads a body into value, rejecting unknown fields so typos fail
// loudly instead of silently booking the wrong thing.
func decodeJSON(writer http.ResponseWriter, request *http.Request, value any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeError(writer, http.StatusBadRequest, "bad_json", fmt.Sprintf("invalid body: %v", err))
		return false
	}
	return true
}

// writeStoreError maps the store's sentinel errors onto HTTP statuses. Any
// unrecognized error is a 500 and the caller should log it.
func writeStoreError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(writer, http.StatusNotFound, "not_found", "no such resource, or it is not yours")
	case errors.Is(err, store.ErrCarNotBookable):
		writeError(writer, http.StatusNotFound, "car_not_bookable", "car does not exist or is not listed")
	case errors.Is(err, store.ErrConflict):
		writeError(writer, http.StatusConflict, "conflict", "car is already booked for an overlapping time")
	case errors.Is(err, store.ErrPriceChanged):
		writeError(writer, http.StatusConflict, "price_changed", "the car's price changed, fetch it again and re-quote")
	case errors.Is(err, store.ErrScheduleConflict):
		writeError(writer, http.StatusConflict, "owner_schedule_conflict", "the owner has this time blocked on a schedule")
	case errors.Is(err, store.ErrTooLateToCancel):
		writeError(writer, http.StatusConflict, "too_late_to_cancel", "bookings cancel up to 24 hours before start, or within an hour of booking")
	default:
		writeError(writer, http.StatusInternalServerError, "internal", "something broke on our side")
	}
}

// statusRecorder captures the status code for metrics.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	recorder.status = status
	recorder.ResponseWriter.WriteHeader(status)
}

func statusClass(status int) string {
	return fmt.Sprintf("%dxx", status/100)
}
