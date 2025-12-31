// Package xsd parses W3C XML Schema documents into a lightly-typed tree that
// mirrors the XSD grammar. It deliberately stops at "faithful tree": turning
// the tree into something a code generator can walk is the job of the ir
// package, which resolves inheritance, groups and references.
package xsd

import "encoding/xml"

// Namespace is the XML Schema namespace every schema document lives in.
const Namespace = "http://www.w3.org/2001/XMLSchema"

// Schema is one parsed .xsd document.
type Schema struct {
	XMLName         xml.Name `xml:"http://www.w3.org/2001/XMLSchema schema"`
	TargetNamespace string   `xml:"targetNamespace,attr"`
	ElementFormDflt string   `xml:"elementFormDefault,attr"`
	AttrFormDflt    string   `xml:"attributeFormDefault,attr"`

	Includes  []Include `xml:"include"`
	Imports   []Import  `xml:"import"`
	Redefines []Include `xml:"redefine"`

	Elements        []*Element        `xml:"element"`
	ComplexTypes    []*ComplexType    `xml:"complexType"`
	SimpleTypes     []*SimpleType     `xml:"simpleType"`
	Attributes      []*Attribute      `xml:"attribute"`
	AttributeGroups []*AttributeGroup `xml:"attributeGroup"`
	Groups          []*Group          `xml:"group"`

	// Location is the path or URL this document was read from. It is filled in
	// by the loader, not by the XML decoder, and is used to resolve the
	// relative schemaLocation of includes and imports.
	Location string `xml:"-"`
	// Prefixes maps the namespace prefixes declared on the schema element to
	// their URIs, so that QName attribute values such as "tns:AddressType" can
	// be resolved. Filled in by the loader.
	Prefixes map[string]string `xml:"-"`
}

// Include is an xs:include or xs:redefine.
type Include struct {
	SchemaLocation string `xml:"schemaLocation,attr"`
}

// Import is an xs:import.
type Import struct {
	Namespace      string `xml:"namespace,attr"`
	SchemaLocation string `xml:"schemaLocation,attr"`
}

// Annotation carries the documentation that becomes doc comments in the
// generated code.
type Annotation struct {
	Documentation []string `xml:"documentation"`
}

// Element is xs:element, either a global declaration or a particle inside a
// content model.
type Element struct {
	Name      string `xml:"name,attr"`
	Ref       string `xml:"ref,attr"`
	Type      string `xml:"type,attr"`
	MinOccurs string `xml:"minOccurs,attr"`
	MaxOccurs string `xml:"maxOccurs,attr"`
	Nillable  string `xml:"nillable,attr"`
	Default   string `xml:"default,attr"`
	Fixed     string `xml:"fixed,attr"`
	Abstract  string `xml:"abstract,attr"`
	SubstFor  string `xml:"substitutionGroup,attr"`
	Form      string `xml:"form,attr"`

	Annotation  *Annotation  `xml:"annotation"`
	ComplexType *ComplexType `xml:"complexType"`
	SimpleType  *SimpleType  `xml:"simpleType"`
}

// Attribute is xs:attribute.
type Attribute struct {
	Name    string `xml:"name,attr"`
	Ref     string `xml:"ref,attr"`
	Type    string `xml:"type,attr"`
	Use     string `xml:"use,attr"`
	Default string `xml:"default,attr"`
	Fixed   string `xml:"fixed,attr"`
	Form    string `xml:"form,attr"`

	Annotation *Annotation `xml:"annotation"`
	SimpleType *SimpleType `xml:"simpleType"`
}

// AttributeGroup is xs:attributeGroup, as a definition or a reference.
type AttributeGroup struct {
	Name string `xml:"name,attr"`
	Ref  string `xml:"ref,attr"`

	Attributes []*Attribute      `xml:"attribute"`
	Groups     []*AttributeGroup `xml:"attributeGroup"`
	AnyAttr    *AnyAttribute     `xml:"anyAttribute"`
}

// AnyAttribute is xs:anyAttribute.
type AnyAttribute struct {
	Namespace string `xml:"namespace,attr"`
}

// Any is xs:any, the wildcard particle.
type Any struct {
	Namespace string `xml:"namespace,attr"`
	MinOccurs string `xml:"minOccurs,attr"`
	MaxOccurs string `xml:"maxOccurs,attr"`
}

// Group is xs:group, as a definition (Name set) or a reference (Ref set).
type Group struct {
	Name      string `xml:"name,attr"`
	Ref       string `xml:"ref,attr"`
	MinOccurs string `xml:"minOccurs,attr"`
	MaxOccurs string `xml:"maxOccurs,attr"`

	Sequence *Particles `xml:"sequence"`
	Choice   *Particles `xml:"choice"`
	All      *Particles `xml:"all"`
}

// Particles is the shared shape of xs:sequence, xs:choice and xs:all. One type
// for all three lets the resolver walk a content model without caring which
// compositor produced it; the compositor only affects whether members are
// mutually exclusive and whether they are individually optional.
//
// The members are held in one ordered slice rather than in a field per kind,
// because document order is part of the meaning of a sequence: a group
// reference between two elements contributes its content at that point, not
// after them. encoding/xml cannot express that with struct fields, so
// Particles decodes itself.
type Particles struct {
	MinOccurs string
	MaxOccurs string
	Items     []Particle
}

// ParticleKind says which member of a Particle is populated.
type ParticleKind int

// The kinds of particle a content model may contain.
const (
	ElementParticle ParticleKind = iota
	GroupParticle
	SequenceParticle
	ChoiceParticle
	AllParticle
	AnyParticle
)

// Particle is one member of a content model, in document order.
type Particle struct {
	Kind    ParticleKind
	Element *Element
	Group   *Group
	Nested  *Particles
	Any     *Any
}

// UnmarshalXML decodes a compositor, preserving the order of its members.
func (p *Particles) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	for _, a := range start.Attr {
		switch a.Name.Local {
		case "minOccurs":
			p.MinOccurs = a.Value
		case "maxOccurs":
			p.MaxOccurs = a.Value
		}
	}
	for {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Space != Namespace {
				if err := d.Skip(); err != nil {
					return err
				}
				continue
			}
			switch t.Name.Local {
			case "element":
				var el Element
				if err := d.DecodeElement(&el, &t); err != nil {
					return err
				}
				p.Items = append(p.Items, Particle{Kind: ElementParticle, Element: &el})
			case "group":
				var g Group
				if err := d.DecodeElement(&g, &t); err != nil {
					return err
				}
				p.Items = append(p.Items, Particle{Kind: GroupParticle, Group: &g})
			case "sequence", "choice", "all":
				var sub Particles
				if err := d.DecodeElement(&sub, &t); err != nil {
					return err
				}
				kind := SequenceParticle
				switch t.Name.Local {
				case "choice":
					kind = ChoiceParticle
				case "all":
					kind = AllParticle
				}
				p.Items = append(p.Items, Particle{Kind: kind, Nested: &sub})
			case "any":
				var any Any
				if err := d.DecodeElement(&any, &t); err != nil {
					return err
				}
				p.Items = append(p.Items, Particle{Kind: AnyParticle, Any: &any})
			default:
				// xs:annotation and anything a later schema version adds.
				if err := d.Skip(); err != nil {
					return err
				}
			}
		case xml.EndElement:
			return nil
		}
	}
}

// ComplexType is xs:complexType.
type ComplexType struct {
	Name     string `xml:"name,attr"`
	Abstract string `xml:"abstract,attr"`
	Mixed    string `xml:"mixed,attr"`

	Annotation     *Annotation       `xml:"annotation"`
	Sequence       *Particles        `xml:"sequence"`
	Choice         *Particles        `xml:"choice"`
	All            *Particles        `xml:"all"`
	Group          *Group            `xml:"group"`
	Attributes     []*Attribute      `xml:"attribute"`
	AttributeGrps  []*AttributeGroup `xml:"attributeGroup"`
	AnyAttr        *AnyAttribute     `xml:"anyAttribute"`
	ComplexContent *ComplexContent   `xml:"complexContent"`
	SimpleContent  *SimpleContent    `xml:"simpleContent"`
}

// ComplexContent is xs:complexContent: derivation from another complex type.
type ComplexContent struct {
	Mixed       string      `xml:"mixed,attr"`
	Extension   *Derivation `xml:"extension"`
	Restriction *Derivation `xml:"restriction"`
}

// SimpleContent is xs:simpleContent: a simple value plus attributes.
type SimpleContent struct {
	Extension   *Derivation `xml:"extension"`
	Restriction *Derivation `xml:"restriction"`
}

// Derivation is the body of an xs:extension or xs:restriction inside complex
// or simple content.
type Derivation struct {
	Base string `xml:"base,attr"`

	Sequence      *Particles        `xml:"sequence"`
	Choice        *Particles        `xml:"choice"`
	All           *Particles        `xml:"all"`
	Group         *Group            `xml:"group"`
	Attributes    []*Attribute      `xml:"attribute"`
	AttributeGrps []*AttributeGroup `xml:"attributeGroup"`
	AnyAttr       *AnyAttribute     `xml:"anyAttribute"`
	Enumerations  []Facet           `xml:"enumeration"`
}

// SimpleType is xs:simpleType.
type SimpleType struct {
	Name string `xml:"name,attr"`

	Annotation  *Annotation        `xml:"annotation"`
	Restriction *SimpleRestriction `xml:"restriction"`
	List        *List              `xml:"list"`
	Union       *Union             `xml:"union"`
}

// List is xs:list: a whitespace-separated sequence of item values.
type List struct {
	ItemType   string      `xml:"itemType,attr"`
	SimpleType *SimpleType `xml:"simpleType"`
}

// Union is xs:union.
type Union struct {
	MemberTypes string        `xml:"memberTypes,attr"`
	SimpleTypes []*SimpleType `xml:"simpleType"`
}

// SimpleRestriction is xs:restriction inside a simple type: a base type
// narrowed by facets.
type SimpleRestriction struct {
	Base       string      `xml:"base,attr"`
	SimpleType *SimpleType `xml:"simpleType"`

	Enumerations []Facet `xml:"enumeration"`
	MinInclusive *Facet  `xml:"minInclusive"`
	MaxInclusive *Facet  `xml:"maxInclusive"`
	MinExclusive *Facet  `xml:"minExclusive"`
	MaxExclusive *Facet  `xml:"maxExclusive"`
	MinLength    *Facet  `xml:"minLength"`
	MaxLength    *Facet  `xml:"maxLength"`
	Length       *Facet  `xml:"length"`
	Pattern      *Facet  `xml:"pattern"`
	FractionDig  *Facet  `xml:"fractionDigits"`
	TotalDigits  *Facet  `xml:"totalDigits"`
}

// Facet is any of the constraining facets; only the value and its annotation
// survive parsing.
type Facet struct {
	Value      string      `xml:"value,attr"`
	Annotation *Annotation `xml:"annotation"`
}
