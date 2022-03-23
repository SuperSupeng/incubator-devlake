package tasks

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/merico-dev/lake/models/domainlayer/didgen"
	"github.com/merico-dev/lake/models/domainlayer/ticket"
	"github.com/merico-dev/lake/plugins/core"
	"github.com/merico-dev/lake/plugins/jira/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SprintIssuesConverter struct {
	db             *gorm.DB
	logger         core.Logger
	sprintIdGen    *didgen.DomainIdGenerator
	issueIdGen     *didgen.DomainIdGenerator
	userIdGen      *didgen.DomainIdGenerator
	sprints        map[string]*ticket.Sprint
	sprintIssue    map[string]*ticket.SprintIssue
	status         map[string]*ticket.IssueStatusHistory
	assignee       map[string]*ticket.IssueAssigneeHistory
	sprintsHistory map[string]*ticket.IssueSprintsHistory
}

func NewSprintIssueConverter(taskCtx core.SubTaskContext) *SprintIssuesConverter {
	return &SprintIssuesConverter{
		db:             taskCtx.GetDb(),
		logger:         taskCtx.GetLogger(),
		sprintIdGen:    didgen.NewDomainIdGenerator(&models.JiraSprint{}),
		issueIdGen:     didgen.NewDomainIdGenerator(&models.JiraIssue{}),
		userIdGen:      didgen.NewDomainIdGenerator(&models.JiraUser{}),
		sprints:        make(map[string]*ticket.Sprint),
		sprintIssue:    make(map[string]*ticket.SprintIssue),
		status:         make(map[string]*ticket.IssueStatusHistory),
		assignee:       make(map[string]*ticket.IssueAssigneeHistory),
		sprintsHistory: make(map[string]*ticket.IssueSprintsHistory),
	}
}

func (c *SprintIssuesConverter) FeedIn(sourceId uint64, cl ChangelogItemResult) {
	if cl.Field == "status" {
		err := c.handleStatus(sourceId, cl)
		if err != nil {
			return
		}
	}
	if cl.Field == "assignee" {
		err := c.handleAssignee(sourceId, cl)
		if err != nil {
			return
		}
	}
	if cl.Field != "Sprint" {
		return
	}
	from, to, err := c.parseFromTo(cl.From, cl.To)
	if err != nil {
		return
	}
	for sprintId := range from {
		err = c.handleFrom(sourceId, sprintId, cl)
		if err != nil {
			c.logger.Error("handle from error:", err)
			return
		}
	}
	for sprintId := range to {
		err = c.handleTo(sourceId, sprintId, cl)
		if err != nil {
			c.logger.Error("handle to error:", err)
			return
		}
	}
}

func (c *SprintIssuesConverter) UpdateSprintIssue() error {
	var err error
	for _, fresh := range c.sprintIssue {
		err = c.db.Updates(fresh).Error
		if err == nil {
			return err
		}
	}
	return nil
}

func (c *SprintIssuesConverter) parseFromTo(from, to string) (map[uint64]struct{}, map[uint64]struct{}, error) {
	fromInts := make(map[uint64]struct{})
	toInts := make(map[uint64]struct{})
	var n uint64
	var err error
	for _, item := range strings.Split(from, ",") {
		s := strings.TrimSpace(item)
		if s == "" {
			continue
		}
		n, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, nil, err
		}
		fromInts[n] = struct{}{}
	}
	for _, item := range strings.Split(to, ",") {
		s := strings.TrimSpace(item)
		if s == "" {
			continue
		}
		n, err = strconv.ParseUint(s, 10, 64)
		if err != nil {
			return nil, nil, err
		}
		toInts[n] = struct{}{}
	}
	inter := make(map[uint64]struct{})
	for k := range fromInts {
		if _, ok := toInts[k]; ok {
			inter[k] = struct{}{}
			delete(toInts, k)
		}
	}
	for k := range inter {
		delete(fromInts, k)
	}
	return fromInts, toInts, nil
}

func (c *SprintIssuesConverter) handleFrom(sourceId, sprintId uint64, cl ChangelogItemResult) error {
	domainSprintId := c.sprintIdGen.Generate(sourceId, sprintId)
	if sprint, _ := c.getSprint(domainSprintId); sprint == nil {
		return nil
	}
	key := fmt.Sprintf("%d:%d:%d", sourceId, sprintId, cl.IssueId)
	if item, ok := c.sprintIssue[key]; ok {
		if item != nil && (item.RemovedDate == nil || item.RemovedDate != nil && item.RemovedDate.Before(cl.Created)) {
			item.RemovedDate = &cl.Created
			item.IsRemoved = true
		}
	} else {
		c.sprintIssue[key] = &ticket.SprintIssue{
			SprintId:    domainSprintId,
			IssueId:     c.issueIdGen.Generate(sourceId, cl.IssueId),
			AddedDate:   nil,
			RemovedDate: &cl.Created,
			IsRemoved:   true,
		}
	}
	k := fmt.Sprintf("%d:%d", sprintId, cl.IssueId)
	if item := c.sprintsHistory[k]; item != nil {
		item.EndDate = &cl.Created
		err := c.db.Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(item).Error
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *SprintIssuesConverter) handleTo(sourceId, sprintId uint64, cl ChangelogItemResult) error {
	domainSprintId := c.sprintIdGen.Generate(sourceId, sprintId)
	if sprint, _ := c.getSprint(domainSprintId); sprint == nil {
		return nil
	}
	key := fmt.Sprintf("%d:%d:%d", sourceId, sprintId, cl.IssueId)
	addedStage, err := c.getStage(cl.Created, domainSprintId)
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		return err
	}
	if addedStage == nil {
		return nil
	}
	if item, ok := c.sprintIssue[key]; ok {
		if item != nil && (item.AddedDate == nil || item.AddedDate != nil && item.AddedDate.After(cl.Created)) {
			item.AddedDate = &cl.Created
			item.AddedStage = addedStage
		}
	} else {
		addedStage, _ := c.getStage(cl.Created, domainSprintId)
		c.sprintIssue[key] = &ticket.SprintIssue{
			SprintId:    domainSprintId,
			IssueId:     c.issueIdGen.Generate(sourceId, cl.IssueId),
			AddedDate:   &cl.Created,
			AddedStage:  addedStage,
			RemovedDate: nil,
		}
	}
	k := fmt.Sprintf("%d:%d", sprintId, cl.IssueId)
	now := time.Now()
	c.sprintsHistory[k] = &ticket.IssueSprintsHistory{
		IssueId:   c.issueIdGen.Generate(sourceId, cl.IssueId),
		SprintId:  domainSprintId,
		StartDate: cl.Created,
		EndDate:   &now,
	}
	return nil
}

func (c *SprintIssuesConverter) getSprint(id string) (*ticket.Sprint, error) {
	if value, ok := c.sprints[id]; ok {
		return value, nil
	}
	var sprint ticket.Sprint
	err := c.db.First(&sprint, "id = ?", id).Error
	if err != nil {
		c.sprints[id] = &sprint
	}
	return &sprint, err
}

func (c *SprintIssuesConverter) getStage(t time.Time, sprintId string) (*string, error) {
	sprint, err := c.getSprint(sprintId)
	if err != nil {
		return nil, err
	}
	return getStage(t, sprint.StartedDate, sprint.CompletedDate), nil
}

func (c *SprintIssuesConverter) handleStatus(sourceId uint64, cl ChangelogItemResult) error {
	var err error
	issueId := c.issueIdGen.Generate(sourceId, cl.IssueId)
	if statusHistory := c.status[issueId]; statusHistory != nil {
		statusHistory.EndDate = &cl.Created
		err = c.db.Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(c.status[issueId]).Error
		if err != nil {
			return err
		}
	}
	now := time.Now()
	c.status[issueId] = &ticket.IssueStatusHistory{
		IssueId:        issueId,
		OriginalStatus: cl.ToString,
		StartDate:      cl.Created,
		EndDate:        &now,
	}
	err = c.db.Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(c.status[issueId]).Error
	if err != nil {
		return err
	}
	return nil
}

func (c *SprintIssuesConverter) handleAssignee(sourceId uint64, cl ChangelogItemResult) error {
	issueId := c.issueIdGen.Generate(sourceId, cl.IssueId)
	if assigneeHistory := c.assignee[issueId]; assigneeHistory != nil {
		assigneeHistory.EndDate = &cl.Created
	}
	var assignee string
	if cl.To != "" {
		assignee = c.userIdGen.Generate(sourceId, cl.To)
	}
	now := time.Now()
	c.assignee[issueId] = &ticket.IssueAssigneeHistory{
		IssueId:   issueId,
		Assignee:  assignee,
		StartDate: cl.Created,
		EndDate:   &now,
	}
	err := c.db.Clauses(clause.OnConflict{
		UpdateAll: true,
	}).Create(c.assignee[issueId]).Error
	if err != nil {
		return err
	}
	return nil
}

func getStage(t time.Time, sprintStart, sprintComplete *time.Time) *string {
	if sprintStart == nil {
		return &ticket.BeforeSprint
	}
	if sprintStart.After(t) {
		return &ticket.BeforeSprint
	}
	if sprintComplete == nil {
		return &ticket.DuringSprint
	}
	if sprintComplete.Before(t) {
		return &ticket.AfterSprint
	}
	return &ticket.DuringSprint
}
