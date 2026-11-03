package forge

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// --- Azure Boards links: peer relations and parent-child hierarchy ---
//
// Both writes read the work item's relations first and send nothing when the
// link is already there, so every re-sync is a read. Targets are compared by
// the trailing id of the relation's url: Azure DevOps hands relations back
// under whichever URL form it likes (org-level, or with a project GUID
// segment), and the id is the only part that names the item.

// The link types dross writes.
const (
	adoRelRelated = "System.LinkTypes.Related"
	adoRelParent  = "System.LinkTypes.Hierarchy-Reverse" // on the child: points at its parent
)

var (
	_ IssueLinker  = (*AzureBoardsClient)(nil)
	_ ParentLinker = (*AzureBoardsClient)(nil)
)

// LinkIssues relates from and to with a Related link — never a hierarchy
// link: backlog and deferred items sit beside their target, not under it
// (c-6). Related is symmetric, so a link already present from either side is
// a no-op.
func (c *AzureBoardsClient) LinkIssues(from, to string) error {
	fromID, toID, err := adoLinkPair(from, to)
	if err != nil {
		return err
	}
	src, err := c.getWorkItem(strconv.Itoa(fromID))
	if err != nil {
		return fmt.Errorf("relate %d to %d: %w", fromID, toID, err)
	}
	if hasRelation(src.relations, adoRelRelated, toID) {
		return nil
	}
	dst, err := c.getWorkItem(strconv.Itoa(toID))
	if err != nil {
		return fmt.Errorf("relate %d to %d: %w", fromID, toID, err)
	}
	if hasRelation(dst.relations, adoRelRelated, fromID) {
		return nil
	}
	patch := adoJSONPatch{{Op: "add", Path: "/relations/-", Value: c.relationValue(adoRelRelated, toID)}}
	if err := c.do(http.MethodPatch, c.projectAPI("/wit/workitems/"+strconv.Itoa(fromID)), patch, nil); err != nil {
		return fmt.Errorf("relate %d to %d: %w", fromID, toID, err)
	}
	return nil
}

// LinkParent puts child under parent with a Hierarchy-Reverse link on the
// child. A child already under parent is left alone (one read, no write). A
// child under a different parent is moved in ONE patch — test the revision
// read, remove the old parent link, add the new one — so a concurrent edit is
// refused rather than overwritten, and the child is never left with two
// parents or none. A refused revision test is an error naming the child; it is
// never retried.
func (c *AzureBoardsClient) LinkParent(parent, child string) error {
	parentID, childID, err := adoLinkPair(parent, child)
	if err != nil {
		return err
	}
	w, err := c.getWorkItem(strconv.Itoa(childID))
	if err != nil {
		return fmt.Errorf("set work item %d's parent: %w", childID, err)
	}
	patch := adoJSONPatch{{Op: "test", Path: "/rev", Value: w.rev}}
	for i, r := range w.relations {
		if !strings.EqualFold(r.rel, adoRelParent) {
			continue
		}
		if relationTargetID(r.url) == parentID {
			return nil
		}
		patch = append(patch, adoPatchOp{Op: "remove", Path: "/relations/" + strconv.Itoa(i)})
		break
	}
	patch = append(patch, adoPatchOp{Op: "add", Path: "/relations/-", Value: c.relationValue(adoRelParent, parentID)})
	if err := c.do(http.MethodPatch, c.projectAPI("/wit/workitems/"+strconv.Itoa(childID)), patch, nil); err != nil {
		return fmt.Errorf("set work item %d's parent to %d: %w", childID, parentID, err)
	}
	return nil
}

// adoLinkPair parses both ends of a link, refusing a non-id or a self-link
// before any request.
func adoLinkPair(a, b string) (int, int, error) {
	aID, err := adoWorkItemID(a)
	if err != nil {
		return 0, 0, err
	}
	bID, err := adoWorkItemID(b)
	if err != nil {
		return 0, 0, err
	}
	if aID == bID {
		return 0, 0, errors.New("azure boards: a work item cannot be linked to itself")
	}
	return aID, bID, nil
}

// relationValue is a relation's patch value: the link type and the target's
// API url.
func (c *AzureBoardsClient) relationValue(rel string, target int) map[string]any {
	return map[string]any{"rel": rel, "url": c.workItemAPIURL(target)}
}

// workItemAPIURL is a work item's API url, the form relations point at.
func (c *AzureBoardsClient) workItemAPIURL(id int) string {
	return c.baseURL + "/_apis/wit/workItems/" + strconv.Itoa(id)
}

// hasRelation reports whether rels holds a rel-typed link to target.
func hasRelation(rels []adoRelation, rel string, target int) bool {
	for _, r := range rels {
		if strings.EqualFold(r.rel, rel) && relationTargetID(r.url) == target {
			return true
		}
	}
	return false
}

// relationTargetID is the work-item id a relation url ends in, or 0.
func relationTargetID(raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		return 0
	}
	p := strings.TrimRight(u.Path, "/")
	id, err := strconv.Atoi(p[strings.LastIndex(p, "/")+1:])
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
