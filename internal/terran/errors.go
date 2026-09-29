package terran

import "errors"

const (
	CodeOperational        = "operational"
	CodeNotEnrolled        = "not_enrolled"
	CodeManifestInvalid    = "manifest_invalid"
	CodeReceiptInvalid     = "receipt_invalid"
	CodeUnsafeState        = "unsafe_state"
	CodeRepositoryMismatch = "repository_mismatch"
	CodePlanChanged        = "plan_changed"
	CodeOverlayUnavailable = "overlay_unavailable"
	CodeUnknownItem        = "unknown_item"
	CodeUsage              = "usage"
)

const (
	nextEnroll   = "run terran enroll --repo <path> --name <ccN>; see README Agent guide"
	nextManifest = "fix terran.json in the catalog, then run terran plan"
	nextState    = "do not edit Terran state; run terran doctor and follow terran-diagnose"
)

type CodedError struct {
	Code    string
	Message string
	Next    string
	err     error
}

func (e *CodedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.err != nil {
		return e.err.Error()
	}
	return e.Code
}

func (e *CodedError) Unwrap() error { return e.err }

func Coded(code, next string, err error) error {
	return &CodedError{Code: code, Next: next, err: err}
}

func ErrorCode(err error) (code, next string) {
	var coded *CodedError
	if errors.As(err, &coded) {
		return coded.Code, coded.Next
	}
	return CodeOperational, ""
}
