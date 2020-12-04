package builder

import (
	"context"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/gateway/client"
	"github.com/pkg/errors"
)

const (
	keyNameProject             = "project"
	keyLocalProject            = "project"
)

func Build(ctx context.Context, c client.Client) (*client.Result, error) {
	opts := c.BuildOpts().Opts
	
	context := llb.Local(keyLocalProject)

	project, ok := opts[keyNameProject]

	if !ok {
		return nil, errors.New("failed to get project name")
	}

//	sourceOp := llb.
//		Image("mcr.microsoft.com/dotnet/aspnet:5.0").
//		Dir("/app")

	sourceOp := llb.
		Image("mcr.microsoft.com/dotnet/sdk:5.0").
		Dir("/src").
		With(
			copyFrom(context, project, "./"),
		).
		Run(llb.Shlexf("dotnet restore \"%s\"", project)).
		With(
			copyAll(context, "."),
		).
		Run(llb.Shlexf("dotnet build \"%s\" -c Release -o /app/build", project))

	publishOp := sourceOp.
		Run(llb.Shlexf("dotnet publish \"%s\" -c Release -o /app/publish", project))

	finalOp := llb.
		Image("mcr.microsoft.com/dotnet/aspnet:5.0").
		Dir("/app").
		With(
			copyFrom(publishOp.State, "/app/publish", "."),
		)

	dt, err := finalOp.Marshal(ctx, llb.LinuxAmd64)

	if err != nil {
		return nil, errors.Wrap(err, "Unable to create definition")
	}

	res, err := c.Solve(ctx, client.SolveRequest{
		Definition: dt.ToPB(),
	})

	if err != nil {
		return nil, errors.Wrap(err, "Unable to solve the definition")
	}

	buildRes := client.NewResult()

	ref, err := res.SingleRef()

	if err != nil {
		return nil, errors.Wrap(err, "Unable to get reference.")
	}

	buildRes.SetRef(ref)

	return buildRes, nil
}

func copyAll(src llb.State, destPath string) llb.StateOption {
	return copyFrom(src, "/.", destPath)
}

// copyFrom has similar semantics as `COPY --from`
func copyFrom(src llb.State, srcPath, destPath string) llb.StateOption {
	return func(s llb.State) llb.State {
		return copy(src, srcPath, s, destPath)
	}
}

// copy copies files between 2 states using cp
func copy(src llb.State, srcPath string, dest llb.State, destPath string) llb.State {
	return dest.File(llb.Copy(src, srcPath, destPath, &llb.CopyInfo{
		AllowWildcard:  true,
		AttemptUnpack:  true,
		CreateDestPath: true,
	}))
}
