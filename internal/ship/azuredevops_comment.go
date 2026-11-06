package ship

import "fmt"

// adoThreadStatus is the status a dross-posted thread is created with. Empty
// sends none: a statusless thread is a plain comment, which keeps parity with
// GitHub, GitLab and Bitbucket, where a PR comment never gates the merge (the
// comment_thread_status lock). Should the live smoke run show a statusless
// thread still counting under a comment-resolution policy, the lock's fallback
// is "closed" — set here, and nowhere else.
const adoThreadStatus = ""

// adoTextComment is Azure DevOps' commentType for a plain text comment.
const adoTextComment = 1

// postAzureDevOpsComment posts the body as a new thread on an Azure Repos PR.
// Azure DevOps has no flat PR comment: every comment lives in a thread, so a
// new top-level comment is a new thread holding one root comment.
func postAzureDevOpsComment(opts CommentOpts) error {
	if opts.PRNumber <= 0 {
		return fmt.Errorf("azure devops comment: PR number %d is not a valid number", opts.PRNumber)
	}
	c, err := adoCommentConn(opts)
	if err != nil {
		return err
	}
	thread := map[string]any{
		"comments": []map[string]any{{
			"parentCommentId": 0,
			"content":         opts.Body,
			"commentType":     adoTextComment,
		}},
	}
	if adoThreadStatus != "" {
		thread["status"] = adoThreadStatus
	}
	endpoint := fmt.Sprintf("%s/pullRequests/%d/threads", c.repoAPI(), opts.PRNumber)
	if _, _, err := adoRequest(c, "POST", endpoint, thread); err != nil {
		return fmt.Errorf("post thread: %w", err)
	}
	return nil
}
