// Package executor is the thing that will execute our rules.
//
// This means processing the rules, one by one, but also ensuring
// dependencies are handled.
//
package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/skx/marionette/file"
	"github.com/skx/marionette/modules"
	"github.com/skx/marionette/parser"
	"github.com/skx/marionette/rules"
)

// Executor holds our internal state.
type Executor struct {

	// Rules are the things we'll execute.
	Rules []rules.Rule

	// PluginDirectories contains an array of directories in which
	// to look for binary/external plugins.
	PluginDirectories []string

	// Index is a mapping between rule-name and index.
	//
	// This is required because we expect users to refer to
	// dependencies by name, but when we search for them in
	// our Rules array above we need to efficiently lookup
	// their index.
	index map[string]int

	// verbose is used to specify if we should be verbose
	// or quiet.
	verbose bool
}

// New creates a new executor, using a series of rules which should have
// been discovered by the parser.
func New(r []rules.Rule) *Executor {

	e := &Executor{Rules: r}

	//
	// Setup plugin paths.
	//
	e.PluginDirectories = append(e.PluginDirectories, "/opt/marionette/plugins")
	e.PluginDirectories = append(e.PluginDirectories, os.Getenv("HOME")+"/.marionette/plugins/")

	return e
}

// SetVerbose enables the verbosity settings to be changed
func (e *Executor) SetVerbose(value bool) {
	e.verbose = value
}

// Get the rules a rule depends upon, via the given key.
//
// This is used to find any `requires` or `notify` rules.
func (e *Executor) deps(rule rules.Rule, key string) []string {

	var res []string

	requires, ok := rule.Params[key]

	// no requirements?  Awesome
	if !ok {
		return res
	}

	//
	// OK the requirements might be a single rule, or
	// an array of rules.
	//
	// Handle both cases.
	//

	str, ok := requires.(string)
	if ok {
		res = append(res, str)
		return res
	}

	strs, ok := requires.([]string)
	if ok {
		return strs
	}

	return res
}

// Check ensures the rules make sense.
//
// In short this means that we check the dependencies/notifiers listed
// for every rule, and raise an error if they contain references to
// rules which don't exist.
func (e *Executor) Check() error {

	// OK at this point we have a list of rules.
	//
	// We want to loop over each one and create a map so that
	// we can lookup rules by name.
	//
	// i.e. If a rule 1 depends upon rule 10 we want to find
	// that out in advance.
	//
	// We'll also make sure we don't try to notify/depend upon
	// a rule that we can't find.
	//
	e.index = make(map[string]int)

	for i, r := range e.Rules {

		_, ok := e.index[r.Name]
		if ok {
			return fmt.Errorf("rule names must be unique; we've already seen '%s'", r.Name)
		}

		e.index[r.Name] = i
	}

	//
	// For every rule.
	//
	for _, r := range e.Rules {

		//
		// Get the dependencies of that rule, and the things
		// it will notify in the event it is triggered.
		//
		deps := e.deps(r, "requires")
		notify := e.deps(r, "notify")

		// Join the pair of rules
		var all []string
		all = append(all, deps...)
		all = append(all, notify...)

		// nothing to check?  Awesome
		if len(all) < 1 {
			continue
		}

		// for each rule-reference
		for _, dep := range all {

			// Does the requirement exist?
			_, found := e.index[dep]
			if !found {
				return fmt.Errorf("rule '%s' has reference to '%s' which doesn't exist", r.Params["name"], dep)
			}
		}
	}

	return nil
}

// Execute runs the rules in turn, handling any dependency ordering.
func (e *Executor) Execute() error {

	// Keep track of which rules we've executed
	seen := make(map[int]bool)

	// For each rule ..
	for i, r := range e.Rules {

		// Don't run rules that are only present to
		// be notified by a trigger.
		if r.Triggered {
			continue
		}

		// Have we executed this rule already?
		if seen[i] {
			continue
		}

		// Get the rule dependencies.
		deps := e.deps(r, "requires")

		// Process each one
		for i, dep := range deps {

			// Have we executed this rule already?
			if seen[i] {
				continue
			}

			// get the actual rule, by index
			dr := e.Rules[e.index[dep]]
			err := e.executeSingleRule(dr)
			if err != nil {
				return err
			}

			// Now we've executed the rule.
			seen[i] = true
		}

		// Now the rule itself
		err := e.executeSingleRule(r)
		if err != nil {
			return err
		}

		// And mark this as executed too.
		seen[i] = true
	}
	return nil
}

// runConditional returns true if the given conditional is true.
func (e *Executor) runConditional(cond interface{}) (bool, error) {

	// Get the value as a string
	test, ok := cond.(*parser.Condition)
	if !ok {
		return false, fmt.Errorf("we expected a conditional structure, but got %v", cond)
	}

	if test.Name == "exists" {

		if len(test.Args) != 1 {
			return false, fmt.Errorf("wrong number of args for 'exists': %d != 1", len(test.Args))
		}

		if file.Exists(test.Args[0]) {
			return true, nil
		}

		return false, nil
	}

	if test.Name == "equal" || test.Name == "equals" {

		if len(test.Args) != 2 {
			return false, fmt.Errorf("wrong number of args for 'equale': %d != 2", len(test.Args))
		}

		if test.Args[0] == test.Args[1] {
			return true, nil
		}
		return false, nil
	}

	return false, fmt.Errorf("unknown conditional-type: %s", test)
}

// executeSingleRule creates the appropriate module, and runs the single rule.
func (e *Executor) executeSingleRule(rule rules.Rule) error {

	// Show what we're doing
	if e.verbose {
		fmt.Printf("Running %s-module rule: %s\n", rule.Type, rule.Name)
	}

	//
	// Are there conditionals present?
	//
	if rule.Params["if"] != nil {
		res, err := e.runConditional(rule.Params["if"])
		if err != nil {
			return err
		}
		if !res {
			if e.verbose {
				fmt.Printf("\tSkipping rule condition was not true: %s\n", rule.Params["if"])
			}
			return nil
		}
	}

	if rule.Params["unless"] != nil {
		res, err := e.runConditional(rule.Params["unless"])
		if err != nil {
			return err
		}
		if res {
			if e.verbose {
				fmt.Printf("\tSkipping rule condition was true: %s\n", rule.Params["unless"])
			}
			return nil
		}
	}

	// Did this rule-execution result in a change?
	//
	// If so then we'd notify any rules which should be executed
	// as a result of that change.
	var changed bool
	var err error

	// Create the instance of the module
	helper := modules.Lookup(rule.Type)
	if helper != nil {

		// Run the module
		changed, err = e.runInternalModule(helper, rule)
		if err != nil {
			return err
		}

	} else {

		// Run the external plugin
		changed, err = e.runBinaryPlugin(rule)
		if err != nil {
			return err
		}
	}

	if changed {

		// Now call any rules that we should notify.
		notify := e.deps(rule, "notify")

		// Process each one
		for _, child := range notify {

			// get the actual rule, by index
			dr := e.Rules[e.index[child]]

			// Execute the rule.
			err := e.executeSingleRule(dr)
			if err != nil {
				return err
			}
		}
	}

	// All done
	return nil
}

// runInternalModule executes the given rule with the loaded internal
// module.
func (e *Executor) runInternalModule(helper modules.ModuleAPI, rule rules.Rule) (bool, error) {

	// Check the arguments
	err := helper.Check(rule.Params)
	if err != nil {
		return false, fmt.Errorf("error validating %s-module rule '%s' %s",
			rule.Type, rule.Name, err.Error())
	}

	// Run the change
	changed, err := helper.Execute(rule.Params)
	if err != nil {
		return false, fmt.Errorf("error running %s-module rule '%s' %s",
			rule.Type, rule.Name, err.Error())
	}

	return changed, nil
}

// runBinaryPlugin invokes our rule with an external binary plugin,
// found upon one of the directories stored in PluginDirectories.
func (e *Executor) runBinaryPlugin(rule rules.Rule) (bool, error) {

	//
	// Look for the file
	//
	path := ""

	for _, dir := range e.PluginDirectories {

		complete := filepath.Join(dir, rule.Type)
		if file.Exists(complete) {
			path = complete
		}
	}

	if path == "" {
		return false, fmt.Errorf("module %s is not built-in, and couldn't be found as an external plugin in directories: %s", rule.Type, strings.Join(e.PluginDirectories, ","))
	}

	//
	// We're looking for an external plugin.
	//
	login := exec.Command(path)

	buffer := bytes.Buffer{}
	result := bytes.Buffer{}
	input, _ := json.Marshal(rule.Params)
	buffer.Write(input)

	login.Stdout = &result
	login.Stdin = &buffer

	err := login.Run()
	if err != nil {
		return false, fmt.Errorf("error running plugin %s (%s) - %s", rule.Type, path, err)
	}

	// What did we get ?
	res := result.String()

	// Did it start with "changed" ?
	if strings.HasPrefix(res, "changed") {
		return true, nil
	}

	return false, nil

}
