package cmd

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"bufio"

	"github.com/Masterminds/semver/v3"
	"gopkg.in/yaml.v1"
)

type Index struct {
	Entries map[string][]Entry
}

type Entry struct {
	Name          string
	Version       string
	Urls          []string
	Locallocation string
}

type Chart struct {
	Dependencies []Dependency
}

type Dependency struct {
	Name       string
	Version    string
	Repository string
}

var repoIndices map[string]Index

func updateDependencies() {
	repoIndices = make(map[string]Index)
	var cacheDir = os.Getenv("HELM_CUSTOM_CACHE")
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) {
		os.Mkdir(cacheDir, 0644)
	}
	if _, err := os.Stat("./charts/"); os.IsNotExist(err) {
		os.Mkdir("./charts", 0644)
	}

	var cacheIndexLocation = cacheDir + "/cache-index.yaml"
	var indexCache Index = unmarshalIndex(cacheIndexLocation)
	var chart = unmarshalChart("./Chart.yaml")
	dependenciesInChartsDir := getDependencyFiles()
	removeDependencies(chart, dependenciesInChartsDir)

	for _, dep1 := range chart.Dependencies {
		if !chartDependencieIsInChartsDir(dependenciesInChartsDir, dep1) {
			versions := make([]*semver.Version, 0)
			location := make(map[*semver.Version]int)
			var indexx Index = getIndexForRepo(dep1.Repository)
			if indexx.Entries[dep1.Name] == nil {
				panic("cannot get entry " + dep1.Name + " from index " + dep1.Repository + ". Make sure you have the " +
					"latest version of the index (helm repo update) and that the dependency can be found.")
			} else {
				for i, entry := range indexx.Entries[dep1.Name] {
					version, _ := semver.NewVersion(entry.Version)
					contstraint, _ := semver.NewConstraint(dep1.Version)
					if contstraint.Check(version) {
						versions = append(versions, version)
						location[version] = i
					}

				}

				if len(versions) == 0 {
					panic("Error: can't get a valid version for repositories airflow. Try changing the version constraint in Chart.yaml" +
						" and make sure you have the latest update from the repository (helm repo update)")
				}
				var cachedEntry Entry
				var foundCachedEntry = false
				_ = sort.Reverse(semver.Collection(versions))
				neededEntry := indexx.Entries[dep1.Name][location[versions[0]]]
				for _, entry := range indexCache.Entries[dep1.Name] {
					if entry.Version == neededEntry.Version {
						//entry found in cache
						cachedEntry = entry
						foundCachedEntry = true
					}
				}
				if !foundCachedEntry {
					dependencyFileName := getDependencyFileName(neededEntry)
					//download dependency
					cmdOutput, _ := exec.Command("curl", neededEntry.Urls[0]).Output()
					os.WriteFile(cacheDir+"/"+dependencyFileName, cmdOutput, 0644)
					//checkError(cmdErr)
					//bijwerken cache index
					neededEntry.Locallocation = getDependencyFileName(neededEntry)

					if indexCache.Entries[dep1.Name] == nil {
						indexCache.Entries[dep1.Name] = make([]Entry, 0)
					}
					indexCache.Entries[dep1.Name] = append(indexCache.Entries[dep1.Name], neededEntry)
					output, err := yaml.Marshal(indexCache)
					checkError(err)
					err = os.WriteFile(cacheIndexLocation, output, 0644)
					checkError(err)
					//entry is now in cache
					cachedEntry = neededEntry

				}
				copyDependency(cachedEntry)
			}
		}
	}

}

func getIndexForRepo(repo string) Index {
	if indexx, ok := repoIndices[repo]; ok {
		return indexx
	} else if strings.HasPrefix(repo, "@") {
		var repoIndexDir = os.Getenv("HELM_REPOSITORY_CACHE")
		indexYamlLocation := repoIndexDir + "/" + repo[1:len(repo)] + "-index.yaml"
		if _, err := os.Stat(indexYamlLocation); os.IsNotExist(err) {
			panic("Error: no repository definition for " + repo + ". Please add them via 'helm repo add'")
		}
		var indexx Index = unmarshalIndex(indexYamlLocation)
		repoIndices[repo] = indexx
		return indexx
	} else {
		//repo must be an url
		repoUrl := repo + "/index.yaml"
		if repo[len(repo)-1:len(repo)] == "/" {
			repoUrl = repo + "index.yaml"
		}
		fmt.Println("Get index from: " + repoUrl)
		fmt.Println("Use aliasses (e.g. @my-repo) in the dependencies in order to improve performance")
		resp, err := http.Get(repoUrl)
		checkError(err)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		var response string
		for i := 0; scanner.Scan(); i++ {
			response = response + scanner.Text() + "\n"
		}
		var indexx Index
		yaml.Unmarshal([]byte(response), &indexx)
		repoIndices[repo] = indexx
		return indexx
	}

}

func removeDependencies(chart Chart, dependencies []Dependency) {
	for _, dependencyInChartDir := range dependencies {
		if !dependencyIsInChart(chart, dependencyInChartDir) {
			e := os.Remove("charts/" + dependencyInChartDir.Name + "-" + dependencyInChartDir.Version + ".tgz")
			checkError(e)
		}
	}
}

func chartDependencieIsInChartsDir(dirDependencies []Dependency, chartDependency Dependency) bool {
	for _, dirDependency := range dirDependencies {
		if dirDependency.Name == chartDependency.Name {
			version, _ := semver.NewVersion(dirDependency.Version)
			contstraint, _ := semver.NewConstraint(chartDependency.Version)
			if contstraint.Check(version) {
				return true
			}
		}
	}
	return false
}

func dependencyIsInChart(chart Chart, dependency Dependency) bool {
	for _, dependencyInChart := range chart.Dependencies {
		if dependency.Name == dependencyInChart.Name {
			version, _ := semver.NewVersion(dependency.Version)
			contstraint, _ := semver.NewConstraint(dependencyInChart.Version)
			if contstraint.Check(version) {
				return true
			}
		}
	}
	return false
}

func getDependencyFiles() []Dependency {
	regex, _ := regexp.Compile("^(.*)-(.*)\\.tgz$")
	dependencies := make([]Dependency, 0)
	files, err := os.ReadDir("charts/")
	if err != nil {
		log.Fatal(err)
	}

	for _, file := range files {
		if !file.IsDir() {
			result := regex.FindAllStringSubmatch(file.Name(), -1)

			if len(result) > 0 {
				var dependency Dependency
				dependency.Name = string(result[0][1])
				dependency.Version = string(result[0][2])
				dependencies = append(dependencies, dependency)
			}
		}
	}
	return dependencies
}

func getDependencyFileName(entry Entry) string {
	return entry.Name + "-" + entry.Version + ".tgz"
}

func copyDependency(cachedEntry Entry) {
	sourceFile := os.Getenv("HELM_CUSTOM_CACHE") + "/" + cachedEntry.Locallocation
	destinationFile := "charts/" + cachedEntry.Locallocation

	input, err := os.ReadFile(sourceFile)
	if err != nil {
		fmt.Println(err)
		return
	}

	err = os.WriteFile(destinationFile, input, 0644)
	if err != nil {
		fmt.Println("Error creating", destinationFile)
		fmt.Println(err)
		return
	}
}

func unmarshalIndex(filename string) Index {
	repoIndex := readFile(filename)
	var indexx Index
	yaml.Unmarshal(repoIndex, &indexx)
	if indexx.Entries == nil {
		indexx.Entries = make(map[string][]Entry)
	}
	return indexx
}

func unmarshalChart(filename string) Chart {
	repoIndex := readFile(filename)
	var chart Chart
	err := yaml.Unmarshal(repoIndex, &chart)
	checkError(err)
	return chart
}

func readFile(filename string) []byte {
	filename, _ = filepath.Abs(filename)
	file, _ := os.ReadFile(filename)
	return file
}

func checkError(err error) {
	if err != nil {
		panic(err)
	}
}
