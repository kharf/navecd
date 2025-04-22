package build

#regexManager: {
	customType: "regex"
	fileMatch: [...string]
	matchStrings: [...string]
	matchStringsStrategy?: string
	depNameTemplate?:      string
	versioningTemplate?:   string
	datasourceTemplate?:   string
	registryUrlTemplate?:  string
	packageNameTemplate?:  string
}

#cueModuleManager: #regexManager & {
	fileMatch: [
		"^(.*?)cue.mod/module.cue$",
		"^(.*?)project/init.go$",
		"^(.*?)testtemplates/templates.go$",
	]
	versioningTemplate: "semver-coerced"
}

_cueModuleVersionManager: #cueModuleManager & {
	matchStrings: [
		"version: \"(?<currentValue>.*?)\"",
		"Language: &modfile.Language{\n(\t*)Version: \"(?<currentValue>.*?)\",\n(\t*)}",
		"const ModuleVersion = \"(?<currentValue>.*?)\"",
	]
	depNameTemplate:    "cue-lang/cue"
	datasourceTemplate: "github-releases"
}

_cueModuleDepVersionManager: #cueModuleManager & {
	matchStrings: [
		"\"github.com/kharf/navecd/schema@v.\": {\n(\t*)v: \"(?<currentValue>.*?)\"\n(\t*)}",
		"\"github.com/kharf/navecd/schema@v.\": {\n(\t*)Version: \"(?<currentValue>.*?)\",\n(\t*)},",
	]
	depNameTemplate:    "kharf/navecd"
	datasourceTemplate: "github-releases"
}

_githubReleaseManager: #regexManager & {
	fileMatch: [
		"^(.*?).go$",
		"^(.*?).cue$",
		"README.md",
	]
	matchStrings: ["https://github.com/(?<depName>.*?)/releases/download/(?<currentValue>.*?)/"]
	datasourceTemplate: "github-releases"
	versioningTemplate: "semver-coerced"
}

labels: [
	"dependencies",
	"renovate",
]
extends: [
	"config:best-practices",
]
dependencyDashboard:   true
semanticCommits:       "enabled"
rebaseWhen:            "auto"
rangeStrategy:         "replace"
branchConcurrentLimit: 0
prConcurrentLimit:     0
prHourlyLimit:         0
"github-actions": enabled: false
ignorePaths: ["dagger/**"]
postUpdateOptions: [
	"gomodTidy",
]
customManagers: [
	_cueModuleVersionManager,
	_cueModuleDepVersionManager,
	{
		customType: "regex"
		fileMatch: [
			"^(.*?).cue$",
		]
		matchStrings: [
			"uses: \"(?<depName>.*?)@(?<currentValue>.*?)\"",
		]
		datasourceTemplate: "github-tags"
		versioningTemplate: "semver-coerced"
	},
	{
		customType: "regex"
		fileMatch: [
			"^(.*?).go$",
		]
		matchStrings: [
			"var kubernetesVersion = \"(?<currentValue>.*?)\"",
		]
		depNameTemplate:    "kubernetes/kubernetes"
		datasourceTemplate: "github-releases"
		versioningTemplate: "semver-coerced"
	},
	{
		customType: "regex"
		fileMatch: [
			"^(.*?).go$",
		]
		matchStrings: ["From\\(\"(?<depName>.*?):(?<currentValue>.*?)\"\\)"]
		datasourceTemplate: "docker"
	},
	{
		customType: "regex"
		fileMatch: [
			"^(.*?).go$",
		]
		matchStrings: [
			"var cueDep = \"(?<depName>.*?)@(?<currentValue>.*?)\"",
			"var controllerGenDep = \"(?<depName>.*?)@(?<currentValue>.*?)\"",
			"var goreleaserDep = \"(?<depName>.*?)@(?<currentValue>.*?)\"",
		]
		datasourceTemplate: "go"
	},
	_githubReleaseManager,
]
