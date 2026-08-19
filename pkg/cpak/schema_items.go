/*
 * Copyright (c) 2025 Fabricators and Mirko Brombin <brombin94@gmail.com>
 * SPDX-License-Identifier: LGPL-2.1-only
 */
package cpak

import "github.com/invopop/jsonschema"

// A few of the manifest's array fields have a shape: a binary is an absolute
// path, a desktop entry is a launcher, an environment entry is NAME=value, a
// legacy host command is a bare command name. Those shapes were written into
// the jsonschema struct tags as "items.pattern", and the schema the validator
// actually runs carried none of them.
//
// The reflector reads "pattern" on an array field and puts it on the items;
// anything it does not recognise it hands to the item type's own tag reader,
// which does not recognise "items.pattern" either, so the keyword was read by
// nobody and dropped without a word. Every one of those fields reached
// gojsonschema as an array of strings with no shape at all.
//
// Renaming the tag fixes three of the four. It cannot fix env: the reflector
// splits an array's tag on every "=", so a pattern containing one is discarded
// before it is even looked at, and NAME=value contains one by definition. The
// shapes therefore live here, in one table, where a test can hold the schema
// to them rather than a struct tag claiming a rule that does not run.

// itemPattern is the shape of one array property's items and where that
// property sits in the reflected schema. An empty definition means the
// manifest itself rather than one of the types it refers to.
type itemPattern struct {
	definition string
	property   string
	pattern    string
}

// holder names where the property lives, for a message a reader can act on.
func (i itemPattern) holder() string {
	if i.definition == "" {
		return "the manifest"
	}
	return i.definition
}

// manifestItemPatterns are drawn to refuse the shape that is wrong, not the
// characters that are unusual. The paths in these fields are the publisher's
// own, and real ones carry spaces, accents, brackets and at signs.
var manifestItemPatterns = []itemPattern{
	// A binary is addressed inside the container image, where a relative path
	// resolves against a working directory nobody here has chosen. Past the
	// leading slash the shape says only what it can: the exported wrapper
	// quotes the path, so all that is left to refuse is a control character,
	// C1 included, since the path is also printed to a terminal by the install
	// prompt. A binary under /opt/Sublime Text is a real one, and so are
	// /usr/bin/caf\u00e9 and /opt/Foo (beta)/bin/foo.
	{property: "binaries", pattern: `^/[^\x00-\x1f\x7f-\x9f]+$`},
	// A launcher, so that a package cannot name the handler database GIO reads
	// out of the same directory it is exported into. desktopEntryExportName
	// says the same thing again at the export, which runs over stored values a
	// schema never sees a second time.
	{property: "desktop_entries", pattern: `^.+\.desktop$`},
	// NAME=value, with the name spelled the way a shell would accept it. The
	// value may be empty: clearing a variable is a thing a package asks for.
	{definition: "Override", property: "env", pattern: `^[A-Za-z_][A-Za-z0-9_]*=`},
	// A command name and not a path to one. The three names the migration
	// accepts are all of this shape.
	{definition: "Override", property: "allowedHostCommands", pattern: `^[A-Za-z0-9_-]+$`},
}

// applyItemPatterns installs the shapes onto a reflected schema. It is silent
// about a property it cannot find, because at runtime there is nothing useful
// to do about one; TestManifestSchemaCarriesEveryItemPattern is what notices.
func applyItemPatterns(schema *jsonschema.Schema) {
	for _, item := range manifestItemPatterns {
		if items := arrayItemSchema(schema, item.definition, item.property); items != nil {
			items.Pattern = item.pattern
		}
	}
}

// arrayItemSchema answers the item schema of one array property.
func arrayItemSchema(schema *jsonschema.Schema, definition, property string) *jsonschema.Schema {
	holder := schema
	if definition != "" {
		holder = schema.Definitions[definition]
	}
	if holder == nil || holder.Properties == nil {
		return nil
	}
	field, ok := holder.Properties.Get(property)
	if !ok || field == nil {
		return nil
	}
	return field.Items
}
