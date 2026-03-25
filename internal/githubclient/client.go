package githubclient

import (
	"context"

	"github.com/google/go-github/v66/github"
	"golang.org/x/oauth2"
)

type Client struct {
	gh *github.Client
}

func New(token string) *Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &Client{gh: github.NewClient(tc)}
}

func (c *Client) GetFile(ctx context.Context, owner, repo, path, ref string) (content string, sha string, exists bool, err error) {
	fc, _, _, err := c.gh.Repositories.GetContents(ctx, owner, repo, path, &github.RepositoryContentGetOptions{Ref: ref})
	if err != nil {
		// If file not found, treat as non-existent.
		if ghErr, ok := err.(*github.ErrorResponse); ok && ghErr.Response != nil && ghErr.Response.StatusCode == 404 {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	if fc == nil {
		return "", "", false, nil
	}
	str, err := fc.GetContent()
	if err != nil {
		return "", "", false, err
	}
	return str, fc.GetSHA(), true, nil
}

func (c *Client) UpsertFile(ctx context.Context, owner, repo, path, branch, message, content, sha string) (commitSHA string, err error) {
	opts := &github.RepositoryContentFileOptions{
		Message: github.String(message),
		Content: []byte(content),
		Branch:  github.String(branch),
	}
	if sha != "" {
		opts.SHA = github.String(sha)
	}
	res, _, err := c.gh.Repositories.UpdateFile(ctx, owner, repo, path, opts)
	if err != nil {
		return "", err
	}
	if res != nil {
		// go-github models this as a struct value, not a pointer
		if sha := res.Commit.GetSHA(); sha != "" {
			return sha, nil
		}
	}
	return "", nil
}
