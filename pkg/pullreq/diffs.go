package pullreq

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/go-github/v30/github"
	"github.com/segmentio/kubeapply/pkg/config"
	log "github.com/sirupsen/logrus"
)

// GetCoveredClusters returns the configs of all clusters that are "covered" by the provided
// diffs. The general approach followed is:
//
// 1. Walk the repo from the root
// 2. Find each cluster config that matches the argument environment
// 3. Get all of the expanded files associated with each config
// 4. Go over all the diffs, mapping them back to the cluster(s) that are configuring them
// 5. Drop any clusters that are not associated with any diffs
// 6. Find the lowest parent among each set of cluster diffs and use this to set the subpath
//    in the associated cluster config
//
// There are a few overrides that adjust this behavior:
// 1. selectedClusterIDs: If set, then clusters in this slice are never dropped, even if they
//    don't have have any diffs in them.
// 2. subpathOverride: If set, then this is used for the cluster subpaths instead of the procedure
//    in step 6 above.
func GetCoveredClusters(
	repoRoot string,
	diffs []*github.CommitFile,
	env string,
	selectedClusterIDs []string,
	subpathOverride string,
) ([]*config.ClusterConfig, error) {
	selectedClusterIDsMap := map[string]struct{}{}
	for _, selectedClusterID := range selectedClusterIDs {
		selectedClusterIDsMap[selectedClusterID] = struct{}{}
	}

	changedClusterPaths := map[string][]string{}

	// Keep map of each config path to its object and all of its files
	configsMap := map[string]*config.ClusterConfig{}
	configFilesMap := map[string][]string{}

	// Walk repo looking for cluster configs
	err := filepath.Walk(
		repoRoot,
		func(path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}

			if strings.HasSuffix(path, ".yaml") {
				fileContents, err := ioutil.ReadFile(path)
				if err != nil {
					return err
				}
				stringContents := string(fileContents)
				if strings.Contains(stringContents, "cluster:") {
					configObj, err := config.LoadClusterConfig(path, repoRoot)
					if err != nil {
						log.Debugf(
							"Error evaluating whether %s is a cluster config: %+v",
							path,
							err,
						)

						// Probably not a cluster config, skip over it
						return nil
					}

					log.Infof("Found cluster config: %s", path)

					if len(selectedClusterIDsMap) > 0 {
						if _, ok := selectedClusterIDsMap[configObj.DescriptiveName()]; !ok {
							log.Infof(
								"Ignoring cluster %s because selectedClusters is set and cluster is not in set",
								configObj.DescriptiveName(),
							)
							return nil
						}
					}

					if configObj.GithubIgnore {
						log.Infof(
							"Ignoring cluster %s because GithubIgnore is true",
							configObj.DescriptiveName(),
						)
						return nil
					}

					if env != "" && configObj.Env != env {
						log.Infof(
							"Ignoring cluster %s because env is not %s",
							configObj.DescriptiveName(),
							env,
						)
						return nil
					}

					relPath, err := filepath.Rel(repoRoot, path)
					if err != nil {
						return err
					}

					configsMap[relPath] = configObj
					configFiles, err := getExpandedConfigFiles(repoRoot, configObj)
					if err != nil {
						return err
					}

					if len(selectedClusterIDsMap) > 0 {
						changedClusterPaths[relPath] = []string{}
					}

					configFilesMap[relPath] = configFiles
				}
			}

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	// Map from each file to the names of the configs that reference it
	configsPerFile := map[string][]string{}

	for configPath, configFiles := range configFilesMap {
		for _, configFile := range configFiles {
			if _, ok := configsPerFile[configFile]; !ok {
				configsPerFile[configFile] = []string{}
			}

			configsPerFile[configFile] = append(configsPerFile[configFile], configPath)
		}
	}

	for _, diff := range diffs {
		diffFile := diff.GetFilename()

		configPaths, ok := configsPerFile[diffFile]
		if ok {
			for _, configPath := range configPaths {
				changedClusterPaths[configPath] = append(
					changedClusterPaths[configPath],
					diffFile,
				)
			}
		}
	}

	log.Infof("Changed cluster paths: %+v", changedClusterPaths)

	changedClusters := []*config.ClusterConfig{}

	for clusterPath, changedFiles := range changedClusterPaths {
		config := configsMap[clusterPath]

		if subpathOverride != "" {
			config.Subpath = subpathOverride
		} else {
			relExpandedPath, err := filepath.Rel(repoRoot, config.ExpandedPath)
			if err != nil {
				return nil, err
			}

			// Override subpath based on files that have changed
			config.Subpath, err = lowestParent(relExpandedPath, changedFiles)
			if err != nil {
				return nil, err
			}
		}

		log.Infof("Setting subpath for cluster %s to %s", clusterPath, config.Subpath)

		changedClusters = append(changedClusters, config)
	}

	// Sort by path
	sort.Slice(
		changedClusters,
		func(a, b int) bool {
			return changedClusters[a].RelPath() < changedClusters[b].RelPath()
		},
	)

	return changedClusters, nil
}

func getExpandedConfigFiles(
	repoRoot string,
	configObj *config.ClusterConfig,
) ([]string, error) {
	configFiles := []string{}

	err := filepath.Walk(
		configObj.ExpandedPath,
		func(path string, info os.FileInfo, err error) error {
			if info.IsDir() {
				return nil
			}

			relPath, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			configFiles = append(configFiles, relPath)
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	return configFiles, nil
}

func lowestParent(root string, paths []string) (string, error) {
	if len(paths) == 0 {
		// If there are no paths, just treat the root as the parent
		return ".", nil
	}

	pathDirs := [][]string{}
	minLen := 0

	for p, path := range paths {
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return "", err
		}

		relDir := filepath.Dir(relPath)
		relDirs := strings.Split(relDir, "/")

		if p == 0 || len(relDirs) < minLen {
			minLen = len(relDirs)
		}

		pathDirs = append(pathDirs, relDirs)
	}

	lowestIndex := -1

outer:
	for i := 0; i < minLen; i++ {
		var currDir string

		for r, relDirs := range pathDirs {
			dir := relDirs[i]

			if r == 0 {
				currDir = dir
			} else if dir != currDir {
				break outer
			}
		}

		lowestIndex = i
	}

	lowestParentPath := "."

	for i := 0; i <= lowestIndex; i++ {
		lowestParentPath = filepath.Join(lowestParentPath, pathDirs[0][i])
	}

	return lowestParentPath, nil
}
