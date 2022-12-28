//go:build requires_docker

package k3d

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/grafana/e2e"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	DefaultK3dImage     = "ghcr.io/k3d-io/k3d:5.4.6-dind"
	internalKubeAPIPort = 6550
)

type K3d struct {
	*e2e.ConcreteService
	sharedDir string
}

func New(image string) (*K3d, error) {
	svc := e2e.NewConcreteService("k3d", image, nil, k3dProbe{}, internalKubeAPIPort)
	svc.SetPrivileged(true)
	return &K3d{ConcreteService: svc}, nil
}

func (k *K3d) Start(networkName, sharedDir string) (err error) {
	k.sharedDir = sharedDir
	return k.ConcreteService.Start(networkName, sharedDir)
}

func (k *K3d) CreateCluster(clusterName string) (*kubernetes.Clientset, error) {
	internalKubeEndpoint := fmt.Sprintf("0.0.0.0:%d", internalKubeAPIPort)
	_, stderr, err := k.Exec(e2e.NewCommand(
		"k3d",
		"cluster", "create",
		"--api-port", internalKubeEndpoint,
		clusterName,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster: %s, stderr:\n%s", err, stderr)
	}

	kubeConfigYAML, stderr, err := k.Exec(e2e.NewCommand("k3d", "kubeconfig", "get", clusterName))
	if err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig: %s, stderr:\n%s", err, stderr)
	}
	kubeConfigYAML = strings.ReplaceAll(kubeConfigYAML, internalKubeEndpoint, k.Endpoint(internalKubeAPIPort))

	kubeConfigFile := filepath.Join(k.sharedDir, "kubeconfig")
	if err := os.WriteFile(kubeConfigFile, []byte(kubeConfigYAML), 0644); err != nil {
		return nil, fmt.Errorf("can't write kubeconfig to %s: %s", kubeConfigFile, err)
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigFile)
	if err != nil {
		return nil, fmt.Errorf("can't build kubernetes client config: %s", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("can't create kubernetes client: %s", err)
	}
	return clientset, nil
}

var k3dVersionRegexp = regexp.MustCompile(`(k3[ds]) version ([^\s]+)`)

// Poor-man's named regexp (yes, we could use actual named regexps, but using more code)
const (
	k3dVersionRegexpComponent = iota + 1
	k3dVersionRegexpVersion
	k3dVersionRegexpLen
)

// PreloadDefaultImages will preload the default images that k3d will use.
// Preloading is useful as host may have these images already downloaded, so we don't have to download them from the internet in dind.
// It will not be aware of any changes made through environment variables. (In the future, this method could be adapted to that)
func (k *K3d) PreloadDefaultImages() error {
	stdout, stderr, err := k.Exec(e2e.NewCommand("k3d", "version"))
	if err != nil {
		return fmt.Errorf("failed getting k3d versions: %s, stderr was:\n%s", err, stderr)
	}
	// stdout should have the following format:
	// k3d version v5.3.0
	// k3s version v1.22.6-k3s1 (default)
	// See: https://github.com/k3d-io/k3d/blob/516fe7c9fde884fe31b3e448a83c06ae46dd331e/cmd/root.go#L210-L230
	submatches := k3dVersionRegexp.FindAllStringSubmatch(stdout, -1)
	if len(submatches) != 2 {
		return fmt.Errorf("couldn't find k3d/k3s versions in stdout:\n%s", stdout)
	}

	// images in k3d are defined here:
	// https://github.com/k3d-io/k3d/blob/516fe7c9fde884fe31b3e448a83c06ae46dd331e/pkg/types/images.go#L33-L79
	// Copying the constants here:
	// DefaultK3sImageRepo specifies the default image repository for the used k3s image
	const DefaultK3sImageRepo = "docker.io/rancher/k3s"
	// DefaultLBImageRepo defines the default cluster load balancer image
	const DefaultLBImageRepo = "ghcr.io/k3d-io/k3d-proxy"
	// DefaultToolsImageRepo defines the default image used for the tools container
	const DefaultToolsImageRepo = "ghcr.io/k3d-io/k3d-tools"

	var images []string
	for si, sub := range submatches {
		if len(sub) != k3dVersionRegexpLen {
			return fmt.Errorf("submatch %d: %v doesn't have exactly %d expected components. stdout:\n%s", si, sub, k3dVersionRegexpLen, stdout)
		}
		switch sub[k3dVersionRegexpComponent] {
		case "k3d":
			// k3d images don't have 'v' prefix in the registry.
			// https://github.com/k3d-io/k3d/blob/516fe7c9fde884fe31b3e448a83c06ae46dd331e/pkg/types/images.go#L78-L78
			version := strings.TrimPrefix(sub[k3dVersionRegexpVersion], "v")
			images = append(images,
				fmt.Sprintf("%s:%s", DefaultLBImageRepo, version),
				fmt.Sprintf("%s:%s", DefaultToolsImageRepo, version),
			)
		case "k3s":
			version := sub[k3dVersionRegexpVersion]
			images = append(images,
				fmt.Sprintf("%s:%s", DefaultK3sImageRepo, version),
			)
		default:
			// Shouldn't happen as regexp only matches k3d and k3s
			return fmt.Errorf("unexpected component matching k3d versions: %s", sub[k3dVersionRegexpComponent])
		}
	}

	for _, image := range images {
		if err := dockerPull(image); err != nil {
			return fmt.Errorf("can't pull image %s: %s", image, err)
		}
		if err := k.loadIntoDIND(image); err != nil {
			return fmt.Errorf("can't load image into dind container: %s", err)
		}
	}

	return nil
}

func (k *K3d) loadIntoDIND(image string) error {
	imageTarFile, err := dockerSave(k.sharedDir, image)
	if err != nil {
		return fmt.Errorf("can't save image %s: %s", image, err)
	}
	savedPath := filepath.Join(e2e.ContainerSharedDir, imageTarFile)
	_, _, err = k.Exec(e2e.NewCommand("docker", "load", "-i", savedPath))
	if err != nil {
		return fmt.Errorf("can't load image %s from %s: %s", image, savedPath, err)
	}
	return nil
}

func dockerPull(image string) error {
	out, err := e2e.RunCommandAndGetOutput("docker", "pull", image)
	if err != nil {
		return fmt.Errorf("%s: output:\n%s", err, string(out))
	}
	return nil
}

var nonWord = regexp.MustCompile(`\W+`)

func dockerSave(sharedDir, image string) (string, error) {
	sum := sha1.Sum([]byte(image))
	shaSumHex := hex.EncodeToString(sum[:])
	filename := nonWord.ReplaceAllString(image, "-") + shaSumHex + ".tar"
	path := filepath.Join(sharedDir, filename)

	out, err := e2e.RunCommandAndGetOutput("docker", "save", "-o", path, image)
	if err != nil {
		return "", fmt.Errorf("failed saving: %s. output: %s", err, string(out))
	}
	return filename, nil
}

// k3dProbe checks that k3d is already running
type k3dProbe struct{}

func (k k3dProbe) Ready(svc *e2e.ConcreteService) error {
	_, _, err := svc.Exec(e2e.NewCommand("k3d", "cluster", "ls"))
	return err
}
