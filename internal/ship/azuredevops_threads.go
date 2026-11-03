package ship

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// --- PR comments on Azure Repos ---
//
// Every Azure DevOps comment lives in a thread. A thread anchored to a file
// (threadContext.filePath) is inline; every other is conversation. Votes,
// pushes and status changes arrive as system comments in threads of their own
// and are not comments anyone wrote, so they are skipped — as are deleted
// comments and deleted threads. Azure DevOps has no bot flag, so every author
// is AuthorUnknown.

// adoCommentIDWidth is how many digits a comment id takes in a listed id. A
// thread id followed by a fixed-width comment id splits back into exactly one
// pair, so thread 1/comment 12 (100012) never meets thread 11/comment 2
// (1100002), and the id stays digits only for prtriage's grammar.
const adoCommentIDWidth = 5

// adoMaxCommentID is the largest comment id the fixed width holds.
const adoMaxCommentID = 99999

// azureDevOpsPRComments reads every human comment across PR n's threads. No
// status filter is sent: resolved threads are listed too, as on every other
// forge (the thread_listing lock). A failure is (nil, err), never the threads
// decoded before the fault.
func azureDevOpsPRComments(opts OpenOpts, n int) ([]PRComment, error) {
	if n <= 0 {
		return nil, fmt.Errorf("PR number %d is not a valid number", n)
	}
	c, err := adoOpenConn(opts)
	if err != nil {
		return nil, err
	}
	rb, _, err := adoRequest(c, "GET", fmt.Sprintf("%s/pullRequests/%d/threads", c.repoAPI(), n), nil)
	if err != nil {
		return nil, fmt.Errorf("read PR #%d threads: %w", n, err)
	}
	type position struct {
		Line int `json:"line"`
	}
	var page struct {
		Value *[]struct {
			ID            int  `json:"id"`
			IsDeleted     bool `json:"isDeleted"`
			ThreadContext *struct {
				FilePath       string    `json:"filePath"`
				RightFileStart *position `json:"rightFileStart"`
				LeftFileStart  *position `json:"leftFileStart"`
			} `json:"threadContext"`
			Comments []struct {
				ID          int             `json:"id"`
				Content     string          `json:"content"`
				CommentType json.RawMessage `json:"commentType"`
				IsDeleted   bool            `json:"isDeleted"`
				Author      *struct {
					ID         string `json:"id"`
					UniqueName string `json:"uniqueName"`
				} `json:"author"`
			} `json:"comments"`
		} `json:"value"`
	}
	if err := json.Unmarshal(rb, &page); err != nil || page.Value == nil {
		return nil, fmt.Errorf("read PR #%d threads: the response is not the thread list it promises", n)
	}
	var out []PRComment
	for _, th := range *page.Value {
		if th.ID <= 0 {
			return nil, fmt.Errorf("read PR #%d threads: a thread carries no id", n)
		}
		if th.IsDeleted {
			continue
		}
		kind, path, line := CommentConversation, "", 0
		if ctx := th.ThreadContext; ctx != nil && ctx.FilePath != "" {
			kind, path = CommentInline, strings.TrimPrefix(ctx.FilePath, "/")
			switch {
			case ctx.RightFileStart != nil && ctx.RightFileStart.Line > 0:
				line = ctx.RightFileStart.Line
			case ctx.LeftFileStart != nil:
				// A comment on a removed line has only the old side.
				line = ctx.LeftFileStart.Line
			}
		}
		for _, cm := range th.Comments {
			if cm.ID <= 0 || cm.ID > adoMaxCommentID {
				return nil, fmt.Errorf("read PR #%d thread %d: comment id %d does not fit a listed id", n, th.ID, cm.ID)
			}
			if cm.IsDeleted || !adoHumanComment(cm.CommentType) {
				continue
			}
			author := CommentAuthor{Bot: AuthorUnknown}
			if cm.Author != nil {
				author.ID = strings.ToLower(cm.Author.ID)
				author.Login = cm.Author.UniqueName
			}
			out = append(out, PRComment{
				ID:     fmt.Sprintf("%d%0*d", th.ID, adoCommentIDWidth, cm.ID),
				Kind:   kind,
				Author: author,
				Path:   path,
				Line:   line,
				URL:    fmt.Sprintf("%s/pullrequest/%d?discussionId=%d", c.repo.URL, n, th.ID),
				Body:   cm.Content,
			})
		}
	}
	return out, nil
}

// adoHumanComment is a commentType someone wrote: text or codeChange, which
// Azure DevOps sends by name or by number (1, 2). system (3) — votes, pushes,
// status changes — and anything unrecognised are not.
func adoHumanComment(raw json.RawMessage) bool {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		switch strings.ToLower(name) {
		case "text", "codechange":
			return true
		}
		return false
	}
	n, err := strconv.Atoi(string(raw))
	return err == nil && (n == 1 || n == 2)
}
