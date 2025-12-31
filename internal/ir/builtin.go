package ir

// builtins maps the XML Schema built-in datatypes onto the small set of
// representations the generators actually distinguish. The derived date and
// numeric types collapse onto their primitives -- xs:integer and
// xs:nonNegativeInteger are both arbitrary-precision in the spec, but every
// real document uses values that fit a 64-bit integer, and generating a
// bignum for them would make the common case unusable.
var builtins = map[string]Builtin{
	"string":             String,
	"normalizedString":   String,
	"token":              String,
	"language":           String,
	"Name":               String,
	"NCName":             String,
	"NMTOKEN":            String,
	"NMTOKENS":           String,
	"ID":                 String,
	"IDREF":              String,
	"IDREFS":             String,
	"ENTITY":             String,
	"ENTITIES":           String,
	"boolean":            Bool,
	"byte":               Byte,
	"short":              Short,
	"int":                Int,
	"long":               Long,
	"integer":            Long,
	"nonPositiveInteger": Long,
	"negativeInteger":    Long,
	"nonNegativeInteger": Long,
	"positiveInteger":    Long,
	"unsignedByte":       UnsignedByte,
	"unsignedShort":      UnsignedShort,
	"unsignedInt":        UnsignedInt,
	"unsignedLong":       UnsignedLong,
	"float":              Float,
	"double":             Double,
	"decimal":            Decimal,
	"dateTime":           DateTime,
	"dateTimeStamp":      DateTime,
	"date":               Date,
	"time":               Time,
	"duration":           Duration,
	"gYear":              String,
	"gYearMonth":         String,
	"gMonth":             String,
	"gMonthDay":          String,
	"gDay":               String,
	"base64Binary":       Base64Binary,
	"hexBinary":          HexBinary,
	"anyURI":             AnyURI,
	"QName":              QName,
	"NOTATION":           QName,
	"anyType":            AnyType,
	"anySimpleType":      String,
}

// builtinFor maps an XSD datatype local name to its representation, defaulting
// to String for anything unrecognized.
func builtinFor(local string) Builtin {
	if b, ok := builtins[local]; ok {
		return b
	}
	return String
}

// isBuiltinName reports whether a local name is an XSD built-in datatype.
func isBuiltinName(local string) bool {
	_, ok := builtins[local]
	return ok
}
