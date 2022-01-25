package tasks

import (
	"fmt"
	"io/ioutil"
	"net/http"

	lakeModels "github.com/merico-dev/lake/models"
	"github.com/merico-dev/lake/plugins/core"
	"github.com/merico-dev/lake/plugins/github/models"
	"github.com/merico-dev/lake/utils"
	"gorm.io/gorm/clause"
)

type ApiCommitsResponse []CommitsResponse
type CommitsResponse struct {
	Sha       string
	Commit    Commit
	Url       string
	Author    *models.GithubUser
	Committer *models.GithubUser
}

type Commit struct {
	Author struct {
		Name  string
		Email string
		Date  core.Iso8601Time
	}
	Committer struct {
		Name  string
		Email string
		Date  core.Iso8601Time
	}
	Message string
}

var RepositoryId int

func HandleCommitsResponse(res *http.Response, done chan bool) error {

	fmt.Println("JON >>> res", res)
	body, err := ioutil.ReadAll(res.Body)
	fmt.Println("JON >>> body in HandleCommitsResponse", string(body))

	githubApiResponse := &ApiCommitsResponse{}
	err = core.UnmarshalResponse(res, githubApiResponse)

	if err != nil || res.StatusCode == 401 {
		fmt.Println("Error >>> err Unmarshalling", err)
		return err
	}
	repoCommit := &models.GithubRepoCommit{GithubRepoId: RepositoryId}
	for _, commit := range *githubApiResponse {
		githubCommit, err := convertGithubCommit(&commit)
		if err != nil {
			fmt.Println("Error >>> err converting commits", err)
			return err
		}
		// save author and committer
		if commit.Author != nil {
			githubCommit.AuthorId = commit.Author.Id
			err = lakeModels.Db.Clauses(clause.OnConflict{
				UpdateAll: true,
			}).Create(&commit.Author).Error
			if err != nil {
				return err
			}
		}
		if commit.Committer != nil {
			githubCommit.CommitterId = commit.Committer.Id
			err = lakeModels.Db.Clauses(clause.OnConflict{
				UpdateAll: true,
			}).Create(&commit.Committer).Error
			if err != nil {
				return err
			}
		}
		err = lakeModels.Db.Clauses(clause.OnConflict{
			UpdateAll: true,
		}).Create(&githubCommit).Error
		if err != nil {
			return err
		}
		// save repo / commit relationship
		repoCommit.CommitSha = commit.Sha
		err = lakeModels.Db.Clauses(clause.OnConflict{
			DoNothing: true,
		}).Create(repoCommit).Error
		if err != nil {
			return err
		}
	}
	done <- true
	return nil
}

func CollectCommits(owner string, repositoryName string, repositoryId int, scheduler *utils.WorkerScheduler, githubApiClient *GithubApiClient) error {
	getUrl := fmt.Sprintf("repos/%v/%v/commits", owner, repositoryName)
	RepositoryId = repositoryId
	done := make(chan bool)
	return githubApiClient.FetchWithPaginationAnts(getUrl, nil, 100, 20, scheduler, func(res *http.Response) error {
		return HandleCommitsResponse(res, done)
	})
}

func convertGithubCommit(commit *CommitsResponse) (*models.GithubCommit, error) {
	githubCommit := &models.GithubCommit{
		Sha:            commit.Sha,
		Message:        commit.Commit.Message,
		AuthorName:     commit.Commit.Author.Name,
		AuthorEmail:    commit.Commit.Author.Email,
		AuthoredDate:   commit.Commit.Author.Date.ToTime(),
		CommitterName:  commit.Commit.Committer.Name,
		CommitterEmail: commit.Commit.Committer.Email,
		CommittedDate:  commit.Commit.Committer.Date.ToTime(),
		Url:            commit.Url,
	}
	return githubCommit, nil
}
