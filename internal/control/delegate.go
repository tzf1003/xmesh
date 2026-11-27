package control

import (
	"errors"
	"net/http"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// DelegateAdmin is the agent's delegation view and local session-reply confirmation surface (API 2.7, 2.8);
// the Agent implements it. Nil on a node without the agent role, and then the capability is not declared.
type DelegateAdmin interface {
	DelegateState() v2.DelegateState
	ListDelegateReplies(state string) (v2.DelegateRepliesResponse, error)
	SubmitDelegateReply(req v2.DelegateReplyCreateRequest) (v2.DelegateReplySummary, error)
	PreviewDelegateReply(replyID string) (v2.DelegateReplyDetail, error)
	ApproveDelegateReply(replyID, bodySHA256 string) (v2.DelegateReplySummary, error)
	RejectDelegateReply(replyID string) (v2.DelegateReplySummary, error)
}

func (s *Server) getDelegate(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.o.Delegate.DelegateState())
}

func (s *Server) listDelegateReplies(w http.ResponseWriter, r *http.Request, _ *call) {
	q := r.URL.Query()
	for k := range q {
		if k != "state" {
			failWith(w, argError(k, "不支持的查询参数"))
			return
		}
	}
	state := q.Get("state")
	out, err := s.o.Delegate.ListDelegateReplies(state)
	if err != nil {
		failWith(w, delegateControlError(err))
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) createDelegateReply(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v2.DelegateReplyCreateRequest
	if err := decode(r, &req, true); err != nil {
		failWith(w, err)
		return
	}
	if len(req.MessageID) != 32 || strings.Trim(req.MessageID, "0123456789abcdef") != "" {
		failWith(w, argError("message_id", "消息编号无效"))
		return
	}
	out, err := s.o.Delegate.SubmitDelegateReply(req)
	if err != nil {
		failWith(w, delegateControlError(err))
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) getDelegateReply(w http.ResponseWriter, r *http.Request, _ *call) {
	replyID := pathPart(r, 3)
	out, err := s.o.Delegate.PreviewDelegateReply(replyID)
	if err != nil {
		failWith(w, delegateControlError(err))
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) approveDelegateReply(w http.ResponseWriter, r *http.Request, _ *call) {
	replyID := pathPart(r, 3)
	var req v2.DelegateReplyApproveRequest
	if err := decode(r, &req, false); err != nil {
		failWith(w, err)
		return
	}
	out, err := s.o.Delegate.ApproveDelegateReply(replyID, req.BodySHA256)
	if err != nil {
		failWith(w, delegateControlError(err))
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) rejectDelegateReply(w http.ResponseWriter, r *http.Request, _ *call) {
	replyID := pathPart(r, 3)
	out, err := s.o.Delegate.RejectDelegateReply(replyID)
	if err != nil {
		failWith(w, delegateControlError(err))
		return
	}
	writeJSON(w, 200, out)
}

func delegateControlError(err error) *v2.Error {
	var te *toolerr.Error
	if !errors.As(err, &te) {
		return newError(v2.ErrInternal, err.Error())
	}
	field, _ := te.Fields["field"].(string)
	reason, _ := te.Fields["reason"].(string)
	rule, _ := te.Fields["rule"].(string)
	switch te.Code {
	case "invalid_args":
		if field == "" {
			field = "body"
		}
		e := argError(field, te.Message)
		e.Reason = reason
		return e
	case "policy_denied":
		e := newError(v2.ErrForbidden, te.Message)
		switch rule {
		case "delegate.disabled":
			e.Reason = "delegate_disabled"
		case "delegate.sessions_disabled":
			e.Reason = "sessions_disabled"
		case "delegate.session_replies_disabled":
			e.Reason = "session_replies_disabled"
		default:
			e.Reason = rule
		}
		return e
	case "not_found":
		return newError(v2.ErrNotFound, te.Message)
	case "conflict", "delegate_saturated":
		e := newError(v2.ErrConflict, te.Message)
		e.Reason = reason
		return e
	default:
		return newError(v2.ErrInternal, te.Message)
	}
}
