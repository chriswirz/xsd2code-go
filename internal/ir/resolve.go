package ir

import (
	"fmt"
	"strings"

	"github.com/chriswirz/xsd2code-go/internal/xsd"
)

// qname is an expanded XML name: the namespace URI plus the local part.
type qname struct{ space, local string }

// decl pairs a declaration with the document that declared it, because
// resolving a QName inside the declaration needs that document's prefix table.
type decl[T any] struct {
	node T
	doc  *xsd.Schema
}

type resolver struct {
	set *xsd.Set
	m   *Model

	complexTypes map[qname]decl[*xsd.ComplexType]
	simpleTypes  map[qname]decl[*xsd.SimpleType]
	elements     map[qname]decl[*xsd.Element]
	attributes   map[qname]decl[*xsd.Attribute]
	groups       map[qname]decl[*xsd.Group]
	attrGroups   map[qname]decl[*xsd.AttributeGroup]

	names *uniquer
	// built caches the Type produced for a named global type, and building
	// guards the recursive descent: a type that refers to itself, directly or
	// through a cycle, must not be built twice.
	built    map[qname]*Type
	building map[qname]string

	choices int
	warned  map[string]bool
}

// Build resolves a loaded schema set into the generator model.
func Build(set *xsd.Set) *Model {
	r := &resolver{
		set:          set,
		m:            &Model{byName: map[string]*Type{}},
		complexTypes: map[qname]decl[*xsd.ComplexType]{},
		simpleTypes:  map[qname]decl[*xsd.SimpleType]{},
		elements:     map[qname]decl[*xsd.Element]{},
		attributes:   map[qname]decl[*xsd.Attribute]{},
		groups:       map[qname]decl[*xsd.Group]{},
		attrGroups:   map[qname]decl[*xsd.AttributeGroup]{},
		names:        newUniquer(),
		built:        map[qname]*Type{},
		building:     map[qname]string{},
		warned:       map[string]bool{},
	}
	if len(set.Roots) > 0 {
		r.m.TargetNamespace = set.Roots[0].TargetNamespace
	}
	for _, miss := range set.Missing {
		r.warn("schema %q was not available; types it declares are treated as strings", miss)
	}
	r.index()
	r.build()
	return r.m
}

func (r *resolver) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if r.warned[msg] {
		return
	}
	r.warned[msg] = true
	r.m.Warnings = append(r.m.Warnings, msg)
}

// index records every global declaration in the set under its expanded name.
func (r *resolver) index() {
	for _, doc := range r.set.All {
		ns := doc.TargetNamespace
		for _, ct := range doc.ComplexTypes {
			if ct.Name != "" {
				r.complexTypes[qname{ns, ct.Name}] = decl[*xsd.ComplexType]{ct, doc}
			}
		}
		for _, st := range doc.SimpleTypes {
			if st.Name != "" {
				r.simpleTypes[qname{ns, st.Name}] = decl[*xsd.SimpleType]{st, doc}
			}
		}
		for _, el := range doc.Elements {
			if el.Name != "" {
				r.elements[qname{ns, el.Name}] = decl[*xsd.Element]{el, doc}
			}
		}
		for _, at := range doc.Attributes {
			if at.Name != "" {
				r.attributes[qname{ns, at.Name}] = decl[*xsd.Attribute]{at, doc}
			}
		}
		for _, g := range doc.Groups {
			if g.Name != "" {
				r.groups[qname{ns, g.Name}] = decl[*xsd.Group]{g, doc}
			}
		}
		for _, ag := range doc.AttributeGroups {
			if ag.Name != "" {
				r.attrGroups[qname{ns, ag.Name}] = decl[*xsd.AttributeGroup]{ag, doc}
			}
		}
	}
}

// build walks the named global types first, then the global elements. Doing
// the named types first means their generated identifiers win any collision
// with a synthesized anonymous name, which keeps output stable when an
// unrelated inline type is added to the schema later.
func (r *resolver) build() {
	for _, doc := range r.set.All {
		ns := doc.TargetNamespace
		for _, ct := range doc.ComplexTypes {
			if ct.Name != "" {
				r.complexType(qname{ns, ct.Name})
			}
		}
		for _, st := range doc.SimpleTypes {
			if st.Name != "" {
				r.simpleType(qname{ns, st.Name}, decl[*xsd.SimpleType]{st, doc})
			}
		}
	}
	for _, doc := range r.set.Roots {
		for _, el := range doc.Elements {
			if el.Name == "" {
				continue
			}
			r.root(el, doc)
		}
	}
}

// root turns a global element declaration into a document entry point.
func (r *resolver) root(el *xsd.Element, doc *xsd.Schema) {
	root := &Root{XMLName: el.Name, Doc: el.Annotation.Doc()}
	if doc.TargetNamespace != "" {
		root.Namespace = doc.TargetNamespace
	}
	switch {
	case el.ComplexType != nil:
		t := r.anonymous(el.ComplexType, Pascal(el.Name), doc)
		root.Type = t.Name
	case el.Type != "":
		ref := r.resolveRef(el.Type, doc, Pascal(el.Name))
		root.Type, root.Builtin = ref.typeName, ref.builtin
	case el.SimpleType != nil:
		ref := r.inlineSimple(el.SimpleType, Pascal(el.Name), doc)
		root.Type, root.Builtin = ref.typeName, ref.builtin
	default:
		// An element with neither a type nor a body is xs:anyType.
		root.Builtin = AnyType
	}
	r.m.Roots = append(r.m.Roots, root)
}

// add registers a new type under a unique generated name.
func (r *resolver) add(t *Type) *Type {
	t.Name = r.names.take(t.Name)
	r.m.Types = append(r.m.Types, t)
	r.m.byName[t.Name] = t
	return t
}

// complexType builds (or returns the cache of) a named global complex type.
func (r *resolver) complexType(qn qname) *Type {
	if t, ok := r.built[qn]; ok {
		return t
	}
	if name, ok := r.building[qn]; ok {
		// Recursive reference. The placeholder name is already final, so the
		// referring field can use it before the type finishes building.
		return &Type{Name: name}
	}
	d, ok := r.complexTypes[qn]
	if !ok {
		return nil
	}
	name := r.names.take(Pascal(d.node.Name))
	r.building[qn] = name

	t := &Type{
		Name:      name,
		XMLName:   d.node.Name,
		Namespace: qn.space,
		Kind:      Class,
		Doc:       d.node.Annotation.Doc(),
		Abstract:  d.node.Abstract == "true",
	}
	r.m.Types = append(r.m.Types, t)
	r.m.byName[name] = t
	r.built[qn] = t
	delete(r.building, qn)

	r.fillComplex(t, d.node, d.doc)
	return t
}

// anonymous builds a type for an inline complexType, naming it after the
// element or attribute that owns it.
func (r *resolver) anonymous(ct *xsd.ComplexType, hint string, doc *xsd.Schema) *Type {
	t := r.add(&Type{
		Name:      hint,
		Namespace: doc.TargetNamespace,
		Kind:      Class,
		Doc:       ct.Annotation.Doc(),
		Abstract:  ct.Abstract == "true",
	})
	r.fillComplex(t, ct, doc)
	return t
}

// fillComplex populates a class from a complexType body, following whichever
// of the four possible content shapes it uses.
func (r *resolver) fillComplex(t *Type, ct *xsd.ComplexType, doc *xsd.Schema) {
	t.Mixed = ct.Mixed == "true"
	fields := newUniquer()
	fields.reserve(t.Name) // a member may not share the name of its class in C#

	switch {
	case ct.ComplexContent != nil:
		cc := ct.ComplexContent
		if cc.Mixed == "true" {
			t.Mixed = true
		}
		if d := cc.Extension; d != nil {
			// Extension is real inheritance: the base keeps its own fields and
			// this type contributes only what it adds.
			if base := r.baseType(d.Base, doc); base != "" {
				t.Base = base
			}
			r.derivation(t, d, doc, fields)
		} else if d := cc.Restriction; d != nil {
			// A restriction must restate the content it keeps, so its own body
			// is the complete content model. Flattening it -- rather than
			// modelling it as inheritance that removes members, which no target
			// language can express -- keeps the generated class honest.
			r.derivation(t, d, doc, fields)
		}
	case ct.SimpleContent != nil:
		sc := ct.SimpleContent
		d := sc.Extension
		if d == nil {
			d = sc.Restriction
		}
		if d != nil {
			// The base is either a simple type, in which case this class is a
			// value plus attributes, or another simpleContent class to inherit
			// from.
			if bqn, ok := r.qname(d.Base, doc); ok && r.isComplex(bqn) {
				if base := r.complexType(bqn); base != nil {
					t.Base = base.Name
				}
			} else {
				ref := r.resolveRef(d.Base, doc, t.Name+"Value")
				t.Fields = append(t.Fields, &Field{
					Name:     fields.take("Value"),
					Origin:   TextField,
					TypeName: ref.typeName,
					Builtin:  ref.builtin,
					List:     ref.list,
				})
			}
			r.attributeList(t, d.Attributes, d.AttributeGrps, d.AnyAttr, doc, fields)
		}
	default:
		r.particles(t, ct.Sequence, doc, fields, false)
		r.particles(t, ct.Choice, doc, fields, true)
		r.particles(t, ct.All, doc, fields, false)
		r.groupRef(t, ct.Group, doc, fields)
		r.attributeList(t, ct.Attributes, ct.AttributeGrps, ct.AnyAttr, doc, fields)
	}

	if t.Mixed && !hasText(t) {
		t.Fields = append(t.Fields, &Field{
			Name:     fields.take("Text"),
			Origin:   TextField,
			Builtin:  String,
			Repeated: len(t.Fields) > 0,
			Doc:      "Character data interleaved with the child elements (mixed content).",
		})
	}
}

func hasText(t *Type) bool {
	for _, f := range t.Fields {
		if f.Origin == TextField {
			return true
		}
	}
	return false
}

// derivation adds the content declared directly on an xs:extension or
// xs:restriction body.
func (r *resolver) derivation(t *Type, d *xsd.Derivation, doc *xsd.Schema, fields *uniquer) {
	r.particles(t, d.Sequence, doc, fields, false)
	r.particles(t, d.Choice, doc, fields, true)
	r.particles(t, d.All, doc, fields, false)
	r.groupRef(t, d.Group, doc, fields)
	r.attributeList(t, d.Attributes, d.AttributeGrps, d.AnyAttr, doc, fields)
}

// baseType resolves the base of a complexContent derivation to a type name.
func (r *resolver) baseType(base string, doc *xsd.Schema) string {
	qn, ok := r.qname(base, doc)
	if !ok {
		return ""
	}
	if qn.space == xsd.Namespace {
		// xs:anyType is the implicit root of every complex type; it carries no
		// members, so inheriting from it would add nothing.
		return ""
	}
	if t := r.complexType(qn); t != nil {
		return t.Name
	}
	r.warn("unknown base type %q; the derived type is generated without a base class", base)
	return ""
}

// particles walks one content model in document order. inChoice makes every
// member optional and tags them with a shared choice number.
func (r *resolver) particles(t *Type, p *xsd.Particles, doc *xsd.Schema, fields *uniquer, inChoice bool) {
	if p == nil {
		return
	}
	choice := 0
	if inChoice {
		r.choices++
		choice = r.choices
	}
	// A repeated compositor makes every member of it repeated: <choice
	// maxOccurs="unbounded"> means each alternative may appear many times.
	group := occurs{
		optional: inChoice || p.MinOccurs == "0",
		repeated: unbounded(p.MaxOccurs),
	}
	for _, item := range p.Items {
		switch item.Kind {
		case xsd.ElementParticle:
			r.elementField(t, item.Element, doc, fields, group, choice)
		case xsd.GroupParticle:
			r.groupRefWith(t, item.Group, doc, fields, group)
		case xsd.ChoiceParticle:
			r.nested(t, item.Nested, doc, fields, group, true)
		case xsd.SequenceParticle, xsd.AllParticle:
			r.nested(t, item.Nested, doc, fields, group, false)
		case xsd.AnyParticle:
			t.Fields = append(t.Fields, &Field{
				Name:     fields.take("Any"),
				Origin:   AnyField,
				Builtin:  AnyType,
				Optional: group.optional || item.Any.MinOccurs == "0",
				Repeated: group.repeated || unbounded(item.Any.MaxOccurs),
				Doc:      "Wildcard content (xs:any), kept as raw XML.",
			})
		}
	}
}

// nested walks a compositor inside another compositor, inheriting the outer
// one's optionality and repetition.
func (r *resolver) nested(t *Type, p *xsd.Particles, doc *xsd.Schema, fields *uniquer, outer occurs, isChoice bool) {
	merged := *p
	if outer.optional {
		merged.MinOccurs = "0"
	}
	if outer.repeated {
		merged.MaxOccurs = "unbounded"
	}
	r.particles(t, &merged, doc, fields, isChoice)
}

// occurs is the optionality and repetition an enclosing compositor forces onto
// the particles inside it.
type occurs struct {
	optional bool
	repeated bool
}

// groupRef expands an xs:group reference into the referring type.
func (r *resolver) groupRef(t *Type, g *xsd.Group, doc *xsd.Schema, fields *uniquer) {
	r.groupRefWith(t, g, doc, fields, occurs{})
}

func (r *resolver) groupRefWith(t *Type, g *xsd.Group, doc *xsd.Schema, fields *uniquer, outer occurs) {
	if g == nil {
		return
	}
	def, defDoc := g, doc
	if g.Ref != "" {
		qn, ok := r.qname(g.Ref, doc)
		if !ok {
			return
		}
		d, found := r.groups[qn]
		if !found {
			r.warn("unknown group %q; its content is omitted", g.Ref)
			return
		}
		def, defDoc = d.node, d.doc
	}
	// Groups are always expanded inline rather than turned into a shared type:
	// XSD groups are a text-substitution device, and the members belong to the
	// class that references them.
	own := occurs{
		optional: outer.optional || g.MinOccurs == "0",
		repeated: outer.repeated || unbounded(g.MaxOccurs),
	}
	for _, p := range []*xsd.Particles{def.Sequence, def.All} {
		if p != nil {
			r.nested(t, p, defDoc, fields, own, false)
		}
	}
	if def.Choice != nil {
		r.nested(t, def.Choice, defDoc, fields, own, true)
	}
}

// elementField turns one element particle into a field.
func (r *resolver) elementField(t *Type, el *xsd.Element, doc *xsd.Schema, fields *uniquer, outer occurs, choice int) {
	def, defDoc := el, doc
	qualified := doc.ElementFormDflt == "qualified" || el.Form == "qualified"

	if el.Ref != "" {
		qn, ok := r.qname(el.Ref, doc)
		if !ok {
			return
		}
		d, found := r.elements[qn]
		if !found {
			r.warn("unknown element ref %q; it is generated as a string field", el.Ref)
			t.Fields = append(t.Fields, &Field{
				Name:     fields.take(Pascal(qn.local)),
				XMLName:  qn.local,
				Origin:   ElementField,
				Builtin:  String,
				Optional: true,
				Choice:   choice,
			})
			return
		}
		// A reference always names a global element, which is qualified.
		def, defDoc = d.node, d.doc
		qualified = defDoc.TargetNamespace != ""
	}
	if def.Name == "" {
		return
	}

	f := &Field{
		Name:     fields.take(Pascal(def.Name)),
		XMLName:  def.Name,
		Origin:   ElementField,
		Doc:      def.Annotation.Doc(),
		Default:  def.Default,
		Fixed:    def.Fixed,
		Nillable: def.Nillable == "true",
		Choice:   choice,
		Optional: outer.optional || el.MinOccurs == "0",
		Repeated: outer.repeated || unbounded(el.MaxOccurs),
	}
	if qualified {
		f.Namespace = defDoc.TargetNamespace
	}
	// A repeated field is never also "optional": an empty collection already
	// says the element was absent, and a nullable collection only invites a
	// null-reference bug in the consuming code.
	if f.Repeated {
		f.Optional = false
	}

	switch {
	case def.ComplexType != nil:
		f.TypeName = r.anonymous(def.ComplexType, t.Name+Pascal(def.Name), defDoc).Name
	case def.SimpleType != nil:
		ref := r.inlineSimple(def.SimpleType, t.Name+Pascal(def.Name), defDoc)
		f.TypeName, f.Builtin, f.List = ref.typeName, ref.builtin, ref.list
	case def.Type != "":
		ref := r.resolveRef(def.Type, defDoc, t.Name+Pascal(def.Name))
		f.TypeName, f.Builtin, f.List = ref.typeName, ref.builtin, ref.list
	default:
		f.Builtin = AnyType
	}
	t.Fields = append(t.Fields, f)
}

// attributeList adds the attributes, expanded attribute groups and wildcard of
// one complexType or derivation body.
func (r *resolver) attributeList(t *Type, attrs []*xsd.Attribute, groups []*xsd.AttributeGroup, any *xsd.AnyAttribute, doc *xsd.Schema, fields *uniquer) {
	for _, at := range attrs {
		r.attributeField(t, at, doc, fields)
	}
	for _, ag := range groups {
		r.attrGroupRef(t, ag, doc, fields, map[qname]bool{})
	}
	if any != nil {
		t.Fields = append(t.Fields, &Field{
			Name:     fields.take("AnyAttributes"),
			Origin:   AnyAttrField,
			Builtin:  String,
			Repeated: true,
			Doc:      "Attributes not declared by the schema (xs:anyAttribute).",
		})
	}
}

func (r *resolver) attrGroupRef(t *Type, ag *xsd.AttributeGroup, doc *xsd.Schema, fields *uniquer, seen map[qname]bool) {
	def, defDoc := ag, doc
	if ag.Ref != "" {
		qn, ok := r.qname(ag.Ref, doc)
		if !ok {
			return
		}
		if seen[qn] {
			return // a self-referential attribute group; expand it once
		}
		seen[qn] = true
		d, found := r.attrGroups[qn]
		if !found {
			r.warn("unknown attributeGroup %q; its attributes are omitted", ag.Ref)
			return
		}
		def, defDoc = d.node, d.doc
	}
	for _, at := range def.Attributes {
		r.attributeField(t, at, defDoc, fields)
	}
	for _, sub := range def.Groups {
		r.attrGroupRef(t, sub, defDoc, fields, seen)
	}
	if def.AnyAttr != nil {
		t.Fields = append(t.Fields, &Field{
			Name:     fields.take("AnyAttributes"),
			Origin:   AnyAttrField,
			Builtin:  String,
			Repeated: true,
			Doc:      "Attributes not declared by the schema (xs:anyAttribute).",
		})
	}
}

func (r *resolver) attributeField(t *Type, at *xsd.Attribute, doc *xsd.Schema, fields *uniquer) {
	if at.Use == "prohibited" {
		return
	}
	def, defDoc := at, doc
	qualified := doc.AttrFormDflt == "qualified" || at.Form == "qualified"
	if at.Ref != "" {
		qn, ok := r.qname(at.Ref, doc)
		if !ok {
			return
		}
		d, found := r.attributes[qn]
		if !found {
			// xml:lang, xml:base and friends live in a namespace nobody ships.
			t.Fields = append(t.Fields, &Field{
				Name:      fields.take(Pascal(qn.local)),
				XMLName:   qn.local,
				Namespace: qn.space,
				Origin:    AttributeField,
				Builtin:   String,
				Optional:  at.Use != "required",
			})
			return
		}
		def, defDoc = d.node, d.doc
		qualified = defDoc.AttrFormDflt == "qualified" || def.Form == "qualified"
	}
	if def.Name == "" {
		return
	}
	f := &Field{
		Name:     fields.take(Pascal(def.Name)),
		XMLName:  def.Name,
		Origin:   AttributeField,
		Doc:      def.Annotation.Doc(),
		Default:  def.Default,
		Fixed:    def.Fixed,
		Optional: at.Use != "required",
	}
	if qualified {
		f.Namespace = defDoc.TargetNamespace
	}
	switch {
	case def.SimpleType != nil:
		ref := r.inlineSimple(def.SimpleType, t.Name+Pascal(def.Name), defDoc)
		f.TypeName, f.Builtin, f.List = ref.typeName, ref.builtin, ref.list
	case def.Type != "":
		ref := r.resolveRef(def.Type, defDoc, t.Name+Pascal(def.Name))
		f.TypeName, f.Builtin, f.List = ref.typeName, ref.builtin, ref.list
	default:
		f.Builtin = String
	}
	t.Fields = append(t.Fields, f)
}

// typeRef is the outcome of resolving a type attribute: either a named model
// type, or a primitive, possibly wrapped in xs:list.
type typeRef struct {
	typeName string
	builtin  Builtin
	list     bool
}

// resolveRef resolves a type="..." or base="..." QName.
func (r *resolver) resolveRef(ref string, doc *xsd.Schema, hint string) typeRef {
	qn, ok := r.qname(ref, doc)
	if !ok {
		r.warn("unresolvable type reference %q; generated as a string", ref)
		return typeRef{builtin: String}
	}
	if qn.space == xsd.Namespace {
		return typeRef{builtin: builtinFor(qn.local)}
	}
	if _, found := r.complexTypes[qn]; found {
		if t := r.complexType(qn); t != nil {
			return typeRef{typeName: t.Name}
		}
	}
	if d, found := r.simpleTypes[qn]; found {
		return r.simpleType(qn, d)
	}
	r.warn("unknown type %q; generated as a string", ref)
	return typeRef{builtin: String}
}

// simpleType resolves a named simple type: an enumeration becomes a generated
// enum, anything else collapses to the primitive it ultimately restricts.
func (r *resolver) simpleType(qn qname, d decl[*xsd.SimpleType]) typeRef {
	if t, ok := r.built[qn]; ok {
		if t.Kind == Enum {
			return typeRef{typeName: t.Name}
		}
		return typeRef{builtin: t.BaseBuiltin}
	}
	if name, ok := r.building[qn]; ok {
		return typeRef{typeName: name}
	}
	r.building[qn] = Pascal(d.node.Name)
	defer delete(r.building, qn)

	ref := r.inlineSimple(d.node, Pascal(d.node.Name), d.doc)
	// Cache under a stub so a second reference does not rebuild the enum.
	stub := &Type{Name: ref.typeName, Kind: Class, BaseBuiltin: ref.builtin}
	if ref.typeName != "" {
		if t := r.m.byName[ref.typeName]; t != nil {
			stub = t
		}
	}
	r.built[qn] = stub
	return ref
}

// inlineSimple resolves any simple type body, named or anonymous.
func (r *resolver) inlineSimple(st *xsd.SimpleType, hint string, doc *xsd.Schema) typeRef {
	switch {
	case st.Restriction != nil:
		res := st.Restriction
		if len(res.Enumerations) > 0 {
			return typeRef{typeName: r.enum(st, res, hint, doc).Name}
		}
		if res.SimpleType != nil {
			return r.inlineSimple(res.SimpleType, hint, doc)
		}
		if res.Base == "" {
			return typeRef{builtin: String}
		}
		return r.resolveRef(res.Base, doc, hint)
	case st.List != nil:
		var item typeRef
		switch {
		case st.List.ItemType != "":
			item = r.resolveRef(st.List.ItemType, doc, hint+"Item")
		case st.List.SimpleType != nil:
			item = r.inlineSimple(st.List.SimpleType, hint+"Item", doc)
		default:
			item = typeRef{builtin: String}
		}
		item.list = true
		return item
	case st.Union != nil:
		// A union has no single representation in a static type system. The
		// lexical form of every member is a string, so a string is the one
		// mapping that always round-trips.
		r.warn("union type %q is generated as a string", hint)
		return typeRef{builtin: String}
	}
	return typeRef{builtin: String}
}

// enum builds a generated enum from an enumeration-restricted simple type.
func (r *resolver) enum(st *xsd.SimpleType, res *xsd.SimpleRestriction, hint string, doc *xsd.Schema) *Type {
	name := hint
	if st.Name != "" {
		name = Pascal(st.Name)
	}
	t := &Type{
		Name:        name,
		XMLName:     st.Name,
		Namespace:   doc.TargetNamespace,
		Kind:        Enum,
		Doc:         st.Annotation.Doc(),
		BaseBuiltin: String,
	}
	if res.Base != "" {
		if base, ok := r.qname(res.Base, doc); ok && base.space == xsd.Namespace {
			t.BaseBuiltin = builtinFor(base.local)
		}
	}
	members := newUniquer()
	for _, e := range res.Enumerations {
		t.Values = append(t.Values, EnumValue{
			Name:  members.take(enumMemberName(e.Value)),
			Value: e.Value,
			Doc:   e.Annotation.Doc(),
		})
	}
	return r.add(t)
}

// enumMemberName turns an enumeration literal into an identifier. Values are
// frequently things like "1", "N/A" or "in-progress", none of which is an
// identifier anywhere.
func enumMemberName(v string) string {
	name := Pascal(v)
	if name == "" || name == "Item" {
		return "Value"
	}
	return name
}

// qname expands a possibly-prefixed QName against the declaring document's
// prefix table.
func (r *resolver) qname(v string, doc *xsd.Schema) (qname, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return qname{}, false
	}
	prefix, local := "", v
	if i := strings.Index(v, ":"); i >= 0 {
		prefix, local = v[:i], v[i+1:]
	}
	if doc != nil && doc.Prefixes != nil {
		if ns, ok := doc.Prefixes[prefix]; ok {
			return qname{ns, local}, true
		}
	}
	if prefix == "" {
		// No default namespace declared: an unprefixed name refers to the
		// document's own target namespace.
		ns := ""
		if doc != nil {
			ns = doc.TargetNamespace
		}
		return qname{ns, local}, true
	}
	// A prefix nobody declared. Assume the XSD namespace when the name is a
	// known primitive, which covers the common typo-free case of a schema that
	// relies on an inherited declaration.
	if isBuiltinName(local) {
		return qname{xsd.Namespace, local}, true
	}
	return qname{}, false
}

// unbounded reports whether a maxOccurs value means "more than one".
func unbounded(max string) bool {
	switch strings.TrimSpace(max) {
	case "", "0", "1":
		return false
	}
	return true
}

// isComplex reports whether an expanded name refers to a global complex type.
func (r *resolver) isComplex(qn qname) bool {
	_, ok := r.complexTypes[qn]
	return ok
}
