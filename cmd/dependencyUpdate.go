package cmd

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

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
	repoIndices = make(map[string]Index)                 // create an empty map to hold repository indices
	var cacheDir = os.Getenv("HELM_CUSTOM_CACHE")        // read the value of the HELM_CUSTOM_CACHE environment variable
	if _, err := os.Stat(cacheDir); os.IsNotExist(err) { // check if the directory specified in HELM_CUSTOM_CACHE exists
		os.Mkdir(cacheDir, 0644) // create the directory with read and write permissions for the owner
	}
	if _, err := os.Stat("./charts/"); os.IsNotExist(err) { // check if the './charts/' directory exists
		os.Mkdir("./charts", 0644) // create the directory with read and write permissions for the owner
	}

	var cacheIndexLocation = cacheDir + "/cache-index.yaml"   // set the location of the cache index file
	var indexCache Index = unmarshalIndex(cacheIndexLocation) // unmarshal the cache index file into an Index struct
	var chart = unmarshalChart("./Chart.yaml")                // unmarshal the './Chart.yaml' file into a Chart struct
	dependenciesInChartsDir := getDependencyFiles()           // get the names of the dependencies in the './charts/' directory
	removeDependencies(chart, dependenciesInChartsDir)        // remove the dependencies that are already present in the './charts/' directory from the Chart struct

	for _, dep1 := range chart.Dependencies { // loop through each dependency in the Chart struct
		if !chartDependencieIsInChartsDir(dependenciesInChartsDir, dep1) { // check if the dependency is not already in the './charts/' directory
			versions := make([]*semver.Version, 0)              // create an empty slice to hold the available versions of the dependency
			location := make(map[*semver.Version]int)           // create an empty map to hold the locations of the available versions of the dependency
			var indexx Index = getIndexForRepo(dep1.Repository) // get the repository index for the dependency
			if indexx.Entries[dep1.Name] == nil {               // check if the repository index contains an entry for the dependency
				panic("cannot get entry " + dep1.Name + " from index " + dep1.Repository + ". Make sure you have the " +
					"latest version of the index (helm repo update) and that the dependency can be found.") // if not, panic and print an error message
			} else { // if an entry for the dependency is found in the repository index
				for i, entry := range indexx.Entries[dep1.Name] { // loop through each entry in the repository index for the dependency
					version, _ := semver.NewVersion(entry.Version)       // parse the version string into a semver.Version struct
					contstraint, _ := semver.NewConstraint(dep1.Version) // parse the version constraint string into a semver.Constraints struct
					if contstraint.Check(version) {                      // check if the version of the entry satisfies the version constraint
						versions = append(versions, version) // if the version of the entry satisfies the version constraint, add it to the slice of available versions
						location[version] = i                // if the version of the entry satisfies the version constraint, add its location in the slice of entries to the map of version locations
					}

				}

				if len(versions) == 0 {
					panic("Error: can't get a valid version for repositories airflow. Try changing the version constraint in Chart.yaml and make sure you have the latest update from the repository (helm repo update)")
				}
				var cachedEntry Entry                                           // create a variable to hold the cached entry for the dependency
				var foundCachedEntry = false                                    // create a flag variable to indicate whether a cached entry for the dependency has been found
				_ = sort.Reverse(semver.Collection(versions))                   // sort the slice of available versions in descending order
				neededEntry := indexx.Entries[dep1.Name][location[versions[0]]] // get the entry for the dependency with the highest available version
				for _, entry := range indexCache.Entries[dep1.Name] {           // loop through each entry in the cache index for the dependency
					if entry.Version == neededEntry.Version { // check if the entry in the cache index matches the needed entry
						//entry found in cache
						cachedEntry = entry     // if a matching entry is found in the cache index, set it as the cached entry for the dependency
						foundCachedEntry = true // set the flag variable to indicate that a cached entry for the dependency has been found
					}
				}
				if !foundCachedEntry { // if a cached entry for the dependency has not been found
					dependencyFileName := getDependencyFileName(neededEntry) // get the file name for the dependency
					//download dependency
					cmdOutput, _ := exec.Command("curl", neededEntry.Urls[0]).Output() // download the dependency from its URL
					os.WriteFile(cacheDir+"/"+dependencyFileName, cmdOutput, 0644)     // write the downloaded dependency to the cache directory
					//checkError(cmdErr)
					//bijwerken cache index
					neededEntry.Locallocation = getDependencyFileName(neededEntry) // set the local location of the needed entry to the file name of the dependency

					if indexCache.Entries[dep1.Name] == nil { // check if the cache index for the dependency is nil
						indexCache.Entries[dep1.Name] = make([]Entry, 0) // if the cache index for the dependency is nil, create a new slice to hold the entries
					}
					indexCache.Entries[dep1.Name] = append(indexCache.Entries[dep1.Name], neededEntry) // append the needed entry to the cache index for the dependency
					output, err := yaml.Marshal(indexCache)                                            // marshal the updated cache index into YAML format
					checkError(err)                                                                    // check for errors during marshaling
					err = os.WriteFile(cacheIndexLocation, output, 0644)                               // write the updated cache index to disk
					checkError(err)                                                                    // check for errors during writing
					//entry is now in cache
					cachedEntry = neededEntry // set the cached entry for the dependency as the needed entry
				}
				copyDependency(cachedEntry) // copy the cached dependency to the './charts/' directory
			}
		}
	}
}
func getIndexForRepo(repo string) Index { // function to get the repository index for a given repository URL or alias
	if indexx, ok := repoIndices[repo]; ok { // check if the repository index has already been loaded and cached
		return indexx // if the repository index has already been loaded and cached, return it
	} else if strings.HasPrefix(repo, "@") { // check if the repository is specified using an alias
		var repoIndexDir = os.Getenv("HELM_REPOSITORY_CACHE")                       // get the path to the local repository cache directory
		indexYamlLocation := repoIndexDir + "/" + repo[1:len(repo)] + "-index.yaml" // set the location of the repository index file
		if _, err := os.Stat(indexYamlLocation); os.IsNotExist(err) {               // check if the repository index file exists
			panic("Error: no repository definition for " + repo + ". Please add them via 'helm repo add'") // if the repository index file does not exist, panic and print an error message
		}
		var indexx Index = unmarshalIndex(indexYamlLocation) // unmarshal the repository index file into an Index struct
		repoIndices[repo] = indexx                           // cache the repository index for the given repository alias
		return indexx                                        // return the repository index for the given repository alias
	} else { // if the repository is specified using a URL
		//repo must be an url
		repoUrl := repo + "/index.yaml"         // set the URL of the repository index file
		if repo[len(repo)-1:len(repo)] == "/" { // check if the repository URL ends with a '/'
			repoUrl = repo + "index.yaml" // if the repository URL ends with a '/', set the URL of the repository index file accordingly
		}
		fmt.Println("Get index from: " + repoUrl) // print a message indicating the repository URL being used
		resp, err := http.Get(repoUrl)            // get the repository index file from the specified URL
		checkError(err)                           // check for errors during the request
		defer resp.Body.Close()
		response, parseYamlError := ioutil.ReadAll(resp.Body)
		if parseYamlError != nil {
			panic("failed to parse yaml from url: " + repoUrl)
		}
		var indexx Index
		yaml.Unmarshal([]byte(response), &indexx) // unmarshal the contents of the response body into an Index struct
		repoIndices[repo] = indexx                // cache the repository index for the given repository URL
		return indexx                             // return the repository index for the given repository URL
	}
}

func removeDependencies(chart Chart, dependencies []Dependency) { // function to remove dependencies that are not in the Chart.yaml file from the local charts directory
	for _, dependencyInChartDir := range dependencies { // iterate over the dependencies in the local charts directory
		if !dependencyIsInChart(chart, dependencyInChartDir) { // check if the dependency is not in the Chart.yaml file
			e := os.Remove("charts/" + dependencyInChartDir.Name + "-" + dependencyInChartDir.Version + ".tgz") // remove the dependency from the local charts directory
			checkError(e)                                                                                       // check for errors during the file removal
		}
	}
}

func chartDependencieIsInChartsDir(dirDependencies []Dependency, chartDependency Dependency) bool { // function to check if a given dependency in the Chart.yaml file is in the local charts directory
	for _, dirDependency := range dirDependencies { // iterate over the dependencies in the local charts directory
		if dirDependency.Name == chartDependency.Name { // check if the names of the dependencies match
			version, _ := semver.NewVersion(dirDependency.Version)
			contstraint, _ := semver.NewConstraint(chartDependency.Version)
			if contstraint.Check(version) { // check if the version of the dependency in the Chart.yaml file matches the version of the dependency in the local charts directory
				return true
			}
		}
	}
	return false
}

func dependencyIsInChart(chart Chart, dependency Dependency) bool { // function to check if a given dependency is in the Chart.yaml file
	for _, dependencyInChart := range chart.Dependencies { // iterate over the dependencies in the Chart.yaml file
		if dependency.Name == dependencyInChart.Name { // check if the names of the dependencies match
			version, _ := semver.NewVersion(dependency.Version)
			contstraint, _ := semver.NewConstraint(dependencyInChart.Version)
			if contstraint.Check(version) { // check if the version of the dependency matches the version of the dependency in the Chart.yaml file
				return true
			}
		}
	}
	return false
}

func getDependencyFiles() []Dependency { // function to get a list of dependencies in the local charts directory
	regex, _ := regexp.Compile("^(.*)-(.*)\\.tgz$") // compile a regex to match the dependency file names
	dependencies := make([]Dependency, 0)           // create an empty slice of Dependency structs
	files, err := os.ReadDir("charts/")             // read the files in the local charts directory
	if err != nil {
		log.Fatal(err)
	}

	for _, file := range files { // iterate over the files in the local charts directory
		if !file.IsDir() { // check if the file is not a directory
			result := regex.FindAllStringSubmatch(file.Name(), -1) // match the file name against the regex

			if len(result) > 0 { // check if the regex matched the file name
				var dependency Dependency
				dependency.Name = string(result[0][1])
				dependency.Version = string(result[0][2])
				dependencies = append(dependencies, dependency) // add the matched dependency to the slice of Dependency structs
			}
		}
	}
	return dependencies
}

func getDependencyFileName(entry Entry) string { // function to get the file name for a cached dependency
	return entry.Name + "-" + entry.Version + ".tgz" // return the file name in the format <dependency name>-<dependency version>
}

func copyDependency(cachedEntry Entry) { // function to copy a cached dependency to the local charts directory
	sourceFile := os.Getenv("HELM_CUSTOM_CACHE") + "/" + cachedEntry.Locallocation // set the source file path to the cached dependency location
	destinationFile := "charts/" + cachedEntry.Locallocation                       // set the destination file path to the local charts directory

	input, err := os.ReadFile(sourceFile) // read the contents of the cached dependency file
	if err != nil {
		fmt.Println(err)
		return
	}

	err = os.WriteFile(destinationFile, input, 0644) // write the contents of the cached dependency file to the local charts directory
	if err != nil {
		fmt.Println("Error creating", destinationFile)
		fmt.Println(err)
		return
	}
}

func unmarshalIndex(filename string) Index { // function to unmarshal an index file into an Index struct
	repoIndex := readFile(filename) // read the contents of the index file
	var indexx Index
	yaml.Unmarshal(repoIndex, &indexx) // unmarshal the YAML data into an Index struct
	if indexx.Entries == nil {         // check if the Entries map is nil
		indexx.Entries = make(map[string][]Entry) // create a new Entries map if it is nil
	}
	return indexx
}

func unmarshalChart(filename string) Chart { // function to unmarshal a Chart.yaml file into a Chart struct
	repoIndex := readFile(filename) // read the contents of the Chart.yaml file
	var chart Chart
	err := yaml.Unmarshal(repoIndex, &chart) // unmarshal the YAML data into a Chart struct
	checkError(err)                          // check for errors during the unmarshalling process
	return chart
}

func readFile(filename string) []byte { // function to read the contents of a file and return it as a byte slice
	filename, _ = filepath.Abs(filename) // get the absolute path of the file
	file, _ := os.ReadFile(filename)     // read the contents of the file
	return file
}

func checkError(err error) { // function to check for errors and panic if an error occurs
	if err != nil {
		panic(err)
	}
}
