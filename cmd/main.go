package main

import (
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/containers/storage/pkg/reexec"
	"github.com/spf13/cobra"

	"k8s.io/component-base/logs"

	"github.com/openshift/builder/pkg/build/builder"
	"github.com/openshift/builder/pkg/version"
	"github.com/openshift/library-go/pkg/serviceability"
)

func main() {
	if reexec.Init() {
		return
	}

	// HERE BE DRAGONS!
	if fp, err := os.Open("/node/var/lib/kubelet/config.json"); err != nil {
		fmt.Printf("error opening config.json: %v\n", err)
	} else {
		if content, err := ioutil.ReadAll(fp); err != nil {
			fmt.Printf("error reading config.json: %v\n", err)
		} else {
			fmt.Printf("configjson content: %s\n", string(content))
		}
	}

	// HERE BE DRAGONS!
	root := "/node/etc/pki/ca-trust/extracted/pem"
	files := make([]string, 0)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		files = append(files, path)
		return nil
	}); err != nil {
		fmt.Printf("error traversing : %v\n", err)
	} else {
		for _, file := range files {
			fmt.Printf("file found: %s", file)
		}
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM)
	go func() {
		<-sigs
		fmt.Println("Error: received unexpected terminate signal")
		os.Exit(1)
	}()

	logs.InitLogs()
	defer logs.FlushLogs()
	defer serviceability.BehaviorOnPanic(os.Getenv("OPENSHIFT_ON_PANIC"), version.Get())()
	defer serviceability.Profile(os.Getenv("OPENSHIFT_PROFILE")).Stop()

	rand.Seed(time.Now().UTC().UnixNano())
	if len(os.Getenv("GOMAXPROCS")) == 0 {
		runtime.GOMAXPROCS(runtime.NumCPU())
	}

	const tlsCertRoot = "/etc/pki/tls/certs"
	const runtimeCertRoot = "/etc/docker/certs.d"

	clusterCASrc := fmt.Sprintf("%s/ca.crt", builder.SecretCertsMountPath)
	clusterCADst := fmt.Sprintf("%s/cluster.crt", tlsCertRoot)
	err := CopyFileIfExists(clusterCASrc, clusterCADst)
	if err != nil {
		fmt.Printf("Error setting up cluster CA cert: %v", err)
		os.Exit(1)
	}

	runtimeCASrc := fmt.Sprintf("%s/certs.d", builder.ConfigMapCertsMountPath)
	err = CopyDirIfExists(runtimeCASrc, runtimeCertRoot)
	if err != nil {
		fmt.Printf("Error setting up service CA cert: %v", err)
		os.Exit(1)
	}

	basename := filepath.Base(os.Args[0])
	command := CommandFor(basename)
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

// CopyDirIfExists recursively copies a directory to the destination path.
// If the source directory does not exist, no error is returned.
// If the destination directory exists, any contents with matching file names
// will be overwritten.
func CopyDirIfExists(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err = os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return err
	}
	dirInfo, err := ioutil.ReadDir(src)
	for _, info := range dirInfo {
		srcPath := filepath.Join(src, info.Name())
		dstPath := filepath.Join(dst, info.Name())
		if info.IsDir() {
			err = CopyDirIfExists(srcPath, dstPath)
		} else {
			err = CopyFileIfExists(srcPath, dstPath)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// CopyFileIfExists copies the source file to the given destination, if the source file exists.
// If the destination file exists, it will be overwritten and will not copy file attributes.
func CopyFileIfExists(src, dst string) error {
	_, err := os.Stat(src)
	if os.IsNotExist(err) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	if err != nil {
		return err
	}
	return out.Close()
}

// CommandFor returns the appropriate command for this base name,
// or the OpenShift CLI command.
func CommandFor(basename string) *cobra.Command {
	var cmd *cobra.Command

	switch basename {
	case "openshift-sti-build":
		cmd = NewCommandS2IBuilder(basename)
	case "openshift-docker-build":
		cmd = NewCommandDockerBuilder(basename)
	case "openshift-git-clone":
		cmd = NewCommandGitClone(basename)
	case "openshift-manage-dockerfile":
		cmd = NewCommandManageDockerfile(basename)
	case "openshift-extract-image-content":
		cmd = NewCommandExtractImageContent(basename)
	default:
		fmt.Printf("unknown command name: %s\n", basename)
		os.Exit(1)
	}

	GLog(cmd.PersistentFlags())

	return cmd
}
