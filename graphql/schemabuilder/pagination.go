package schemabuilder

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/samsarahq/thunder/graphql"
	"reflect"
)

// Connection conforms to the GraphQL Connection type in the Relay Pagination spec.
type Connection struct {
	TotalCount int64
	Edges      []Edge
	PageInfo   PageInfo
}

// PageInfo contains information for pagination on a connection type. The list of Pages is used for
// page-number based pagination where the ith index corresponds to the start cursor of (i+1)st page.
type PageInfo struct {
	HasNextPage bool
	EndCursor   string
	HasPrevPage bool
	StartCursor string
	Pages       []string
}

// Edge consists of a node paired with its b64 encoded cursor.
type Edge struct {
	Node   interface{}
	Cursor string
}

// ConnectionArgs conform to the pagination arguments as specified by the Relay Spec for Connection
// types. The Args field consits of the user-facing args.
type ConnectionArgs struct {
	First  *int64
	Last   *int64
	After  *string
	Before *string
	Args   interface{}
}

func getTypeName(typ reflect.Type) string {
	if typ.Kind() == reflect.Ptr {
		return typ.Elem().Name()
	}
	return fmt.Sprintf("NonNull%s", typ.Name())
}

// constructEdgeType wraps the typ (which is the type of the Node) in an Edge type conforming to the
// Relay spec.
func (sb *schemaBuilder) constructEdgeType(typ reflect.Type) (graphql.Type, error) {
	nodeType, err := sb.getType(typ)
	if err != nil {
		return nil, err
	}

	fieldMap := make(map[string]*graphql.Field)

	nodeField := &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			if value, ok := source.(Edge); ok {
				return value.Node, nil
			}

			return nil, fmt.Errorf("error resolving node in edge")

		},
		Type:           nodeType,
		ParseArguments: nilParseArguments,
	}
	fieldMap["node"] = nodeField

	cursorType, err := sb.getType(reflect.TypeOf(string("")))
	if err != nil {
		return nil, err
	}

	cursorField := &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			if value, ok := source.(Edge); ok {
				return value.Cursor, nil
			}
			return nil, fmt.Errorf("error resolving cursor in edge")
		},
		Type:           cursorType,
		ParseArguments: nilParseArguments,
	}

	fieldMap["cursor"] = cursorField

	return &graphql.NonNull{
		Type: &graphql.Object{
			Name:        fmt.Sprintf("%sEdge", getTypeName(typ)),
			Description: "",
			Fields:      fieldMap,
		},
	}, nil

}

// constructConnType wraps typ (type of the Node) in a Connection Type conforming to the Relay spec.
func (funcCtx *funcContext) constructConnType(sb *schemaBuilder, typ reflect.Type) (graphql.Type, error) {
	fieldMap := make(map[string]*graphql.Field)

	countType, _ := reflect.TypeOf(Connection{}).FieldByName("TotalCount")
	countField, err := sb.buildField(countType)
	if err != nil {
		return nil, err
	}

	fieldMap["totalCount"] = countField

	edgeType, err := sb.constructEdgeType(typ)
	if err != nil {
		return nil, err
	}

	edgesSliceType := &graphql.NonNull{Type: &graphql.List{Type: edgeType}}

	edgesSliceField := &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			if value, ok := source.(Connection); ok {
				return value.Edges, nil
			}
			return nil, fmt.Errorf("error resolving edges in connection")
		},
		Type:           edgesSliceType,
		ParseArguments: nilParseArguments,
	}

	fieldMap["edges"] = edgesSliceField

	pageInfoType, _ := reflect.TypeOf(Connection{}).FieldByName("PageInfo")
	pageInfoField, err := sb.buildField(pageInfoType)
	if err != nil {
		return nil, err
	}
	fieldMap["pageInfo"] = pageInfoField
	retObject := &graphql.NonNull{
		Type: &graphql.Object{
			Name:        fmt.Sprintf("%sConnection", getTypeName(typ)),
			Description: "",
			Fields:      fieldMap,
		},
	}
	return retObject, nil
}

// EdgesToReturn returns the slice of edges by appyling the pagination arguments. It also returns
// the hasNextPage and hasPrevPage values respectively. The behavior is expected to conform to the
// Relay Cursor spec: https://facebook.github.io/relay/graphql/connections.htm#EdgesToReturn()
func EdgesToReturn(allEdges []Edge, before *string, after *string, first *int64, last *int64) ([]Edge, bool, bool, error) {
	edges, elemsAfter, elemsBefore := applyCursorsToAllEdges(allEdges, before, after)

	prevPage := false
	nextPage := false

	if first != nil {
		if *first < 0 {
			return nil, nextPage, prevPage, graphql.NewClientError("first should be a non-negative integer")
		}
		if len(edges) > int(*first) {
			edges = edges[:int(*first)]
			nextPage = true
		}
	}
	if before != nil {
		if elemsAfter {
			nextPage = true
		}
	}

	if last != nil {
		if *last < 0 {
			return nil, nextPage, prevPage, graphql.NewClientError("last should be a non-negative integer")
		}
		if len(edges) > int(*last) {
			edges = edges[len(edges)-int(*last):]
			prevPage = true
		}
	}
	if after != nil {
		if elemsBefore {
			prevPage = true
		}
	}

	return edges, nextPage, prevPage, nil
}

// getCursorIndex returns the index corresponding to the cursor in the slice.
func getCursorIndex(edges []Edge, cursor string) int {
	for i, val := range edges {
		if val.Cursor == cursor {
			return i
		}
	}
	return -1
}

// applyCursorsToAllEdges returns the slice of edges after applying the after and before arguments.
// It also implements part of the hasNextPage and hasPrevPage algorithm by returning if there are
// elements after or before the arguments.
func applyCursorsToAllEdges(allEdges []Edge, before *string, after *string) ([]Edge, bool, bool) {
	edges := allEdges

	elemsAfter := false
	elemsBefore := false

	if after != nil {
		i := getCursorIndex(edges, *after)
		if i != -1 {
			edges = edges[i+1:]
			if i != 0 {
				elemsBefore = true
			}
		}

	}
	if before != nil {
		i := getCursorIndex(edges, *before)
		if i != -1 {
			edges = edges[:i]
			if i != len(allEdges)-1 {
				elemsAfter = true
			}
		}

	}

	return edges, elemsAfter, elemsBefore

}

// getConnection applies the ConnectionArgs to nodes and returns the result in a wrapped Connection
// type.
func getConnection(key string, nodes []interface{}, args ConnectionArgs) (Connection, error) {
	var edges []Edge

	lim := int64(0)
	if args.First != nil {
		lim = *args.First
	} else if args.Last != nil {
		lim = *args.Last
	}

	var pages []string
	if len(nodes) > 0 {
		pages = append(pages, "")
	}
	for i, val := range nodes {
		// Get the value of the key field and then b64 encode it for the cursor.
		keyValue := reflect.ValueOf(val)
		if keyValue.Kind() == reflect.Ptr {
			keyValue = keyValue.Elem()
		}
		keyString := []byte(fmt.Sprintf("%v", keyValue.FieldByName(key).Interface()))
		cursorVal := base64.StdEncoding.EncodeToString(keyString)
		// If the next cursor is the start cursor of a page then push the current cursor to the
		// list. If an end cursor is the last cursor, then it cannot be followed by a page.
		if lim != 0 && i != len(nodes)-1 && (int64(i+1)%lim) == 0 {
			pages = append(pages, cursorVal)
		}
		edges = append(edges, Edge{Node: val, Cursor: cursorVal})
	}

	edges, nextPage, prevPage, err := EdgesToReturn(edges, args.Before, args.After, args.First, args.Last)
	if err != nil {
		return Connection{}, err
	}

	endCursor := ""
	if len(edges) > 0 {
		endCursor = edges[len(edges)-1].Cursor
	}
	startCursor := ""
	if len(edges) > 0 {
		startCursor = edges[0].Cursor
	}

	pageInfo := PageInfo{HasNextPage: nextPage, EndCursor: endCursor, StartCursor: startCursor, HasPrevPage: prevPage, Pages: pages}

	return Connection{TotalCount: int64(len(nodes)), Edges: edges, PageInfo: pageInfo}, nil
}

// PaginateFieldFunc registers a function that is also paginated according to the Relay
// Connection Spec. The field is registered as a Connection Type and first, last, before and after
// are automatically added as arguments to the function. The return type to the function must be a
// list. The element of the list is wrapped as a Node Type.
func (o *Object) PaginateFieldFunc(name string, f interface{}) {
	o.paginatedFields = append(o.paginatedFields,
		paginationObject{
			Name: name,
			Fn:   f,
		})
}

func (funcCtx *funcContext) consumePaginatedArgs(sb *schemaBuilder, in []reflect.Type) (*argParser, graphql.Type, []reflect.Type, error) {
	var argParser *argParser
	var argType graphql.Type
	var err error
	if len(in) > 0 && in[0] != selectionSetType {
		if argParser, argType, err = sb.buildPaginatedArgParser(in[0]); err != nil {
			return nil, nil, nil, err
		}
		in = in[1:]
	} else {
		if argParser, argType, err = sb.buildPaginatedArgParser(nil); err != nil {
			return nil, nil, in, err
		}
	}

	return argParser, argType, in, nil

}

func (sb *schemaBuilder) getKeyFieldOnStruct(nodeType reflect.Type) (string, error) {

	nodeObj := sb.objects[nodeType]
	if nodeObj == nil && nodeType.Kind() == reflect.Ptr {
		nodeObj = sb.objects[nodeType.Elem()]
	}
	if nodeObj == nil {
		return "", fmt.Errorf("%s must be a struct and registered as an object along with its key", nodeType)
	}
	nodeKey := reverseGraphqlFieldName(nodeObj.key)
	if nodeKey == "" {
		return nodeKey, fmt.Errorf("a key field must be registered for paginated objects")
	}
	if nodeType.Kind() == reflect.Ptr {
		nodeType = nodeType.Elem()
	}
	if _, ok := nodeType.FieldByName(nodeKey); !ok {
		return nodeKey, fmt.Errorf("field doesn't exist on struct")
	}

	return nodeKey, nil

}

// buildPaginatedField corresponds to buildFunction on a paginated type. It wraps the return result
// of f in a connection type.
func (sb *schemaBuilder) buildPaginatedField(typ reflect.Type, f interface{}) (*graphql.Field, error) {
	funcCtx := &funcContext{typ: typ}

	fun, err := funcCtx.getFuncVal(&method{Fn: f})
	if err != nil {
		return nil, err
	}

	in := funcCtx.getFuncInputTypes()
	in = funcCtx.consumeContextAndSource(in)

	argParser, argType, in, err := funcCtx.consumePaginatedArgs(sb, in)
	if err != nil {
		return nil, err
	}
	funcCtx.hasArgs = true

	in = funcCtx.consumeSelectionSet(in)

	// We have succeeded if no arguments remain.
	if len(in) != 0 {
		return nil, fmt.Errorf("%s arguments should be [context][, [*]%s][, args][, selectionSet]", funcCtx.funcType, typ)
	}

	// Parse return values. The first return value must be the actual value, and
	// the second value can optionally be an error.
	if err := funcCtx.parseReturnSignature(&method{MarkedNonNullable: true}); err != nil {
		return nil, err
	}

	// It's safe to assume that there's a return type since the method is marked as non-nullable
	// when calling parseReturnSignature above.
	if funcCtx.funcType.Out(0).Kind() != reflect.Slice {
		return nil, fmt.Errorf("paginated field func must return a slice type")
	}
	nodeType := funcCtx.funcType.Out(0).Elem()
	retType, err := funcCtx.constructConnType(sb, nodeType)
	if err != nil {
		return nil, err
	}

	nodeKey, err := sb.getKeyFieldOnStruct(nodeType)
	if err != nil {
		return nil, err
	}

	args, err := funcCtx.argsTypeMap(argType)

	ret := &graphql.Field{
		Resolve: func(ctx context.Context, source, args interface{}, selectionSet *graphql.SelectionSet) (interface{}, error) {
			val, ok := args.(ConnectionArgs)
			if !ok {
				return nil, fmt.Errorf("arguments should implement ConnectionArgs")
			}
			funcCtx.hasArgs = val.Args != nil
			var argsVal interface{}
			if funcCtx.hasArgs {
				argsVal = reflect.ValueOf(val.Args).Elem().Interface()
			}

			in := funcCtx.prepareResolveArgs(source, argsVal, ctx)

			// Call the function.
			out := fun.Call(in)

			return funcCtx.extractPaginatedRetAndErr(nodeKey, out, args, retType)

		},
		Args:           args,
		Type:           retType,
		ParseArguments: argParser.Parse,
		Expensive:      funcCtx.hasContext,
	}

	return ret, nil
}

func (funcCtx *funcContext) extractPaginatedRetAndErr(nodeKey string, out []reflect.Value, args interface{}, retType graphql.Type) (interface{}, error) {
	var result interface{}
	connectionArgs, _ := args.(ConnectionArgs)

	result, err := getConnection(nodeKey, castSlice(out[0].Interface()), connectionArgs)
	if err != nil {
		return nil, err
	}
	out = out[1:]
	if funcCtx.hasError {
		if err := out[0]; !err.IsNil() {
			return nil, err.Interface().(error)
		}
	}

	return result, nil
}

func castSlice(slice interface{}) []interface{} {
	s := reflect.ValueOf(slice)
	if s.Kind() != reflect.Slice {
		panic("cast given a non-slice type")
	}

	ret := make([]interface{}, s.Len())
	for i := 0; i < s.Len(); i++ {
		ret[i] = s.Index(i).Interface()
	}

	return ret
}

// buildPaginatedArgParser corresponds to buildArgParser for arguments used on a paginated
// fieldFunc. The args are nested as the Args field in the ConnectionArgs.
func (sb *schemaBuilder) buildPaginatedArgParser(originalArgType reflect.Type) (*argParser, graphql.Type, error) {
	//nestedArgParser and nestedArgType are used for building the parser function for the args
	//passed in to the paginated field.
	typ := reflect.TypeOf(ConnectionArgs{})

	// Fields build a map of the fields for ConnectionArgs.
	fields := make(map[string]argField)

	argType := &graphql.InputObject{
		Name:        typ.Name(),
		InputFields: make(map[string]graphql.Type),
	}

	argType.Name += "_InputObject"

	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)

		// The field which is of type interface should only be one and will be used to parse the
		// original function args.
		if field.Type.Kind() == reflect.Interface {
			continue
		}

		name := makeGraphql(field.Name)

		var parser *argParser
		var fieldArgTyp graphql.Type

		parser, fieldArgTyp, err := sb.makeArgParser(field.Type)
		if err != nil {
			return nil, nil, err
		}

		argType.InputFields[name] = fieldArgTyp

		fields[name] = argField{
			field:  field,
			parser: parser,
		}
	}

	var nestedArgParser *argParser
	var nestedArgType graphql.Type
	var err error
	if originalArgType != nil {
		nestedArgParser, nestedArgType, err = sb.makeStructParser(originalArgType)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to build args for paginated field")
		}
		userInputObject, ok := nestedArgType.(*graphql.InputObject)
		if !ok {
			return nil, nil, fmt.Errorf("args should be an object")
		}

		for name, typ := range userInputObject.InputFields {
			argType.InputFields[name] = typ
		}
	}

	return &argParser{
		FromJSON: func(value interface{}, dest reflect.Value) error {
			asMap, ok := value.(map[string]interface{})
			if !ok {
				return errors.New("not an object")
			}

			for name, field := range fields {
				value := asMap[name]
				fieldDest := dest.FieldByIndex(field.field.Index)
				if err := field.parser.FromJSON(value, fieldDest); err != nil {
					return fmt.Errorf("%s: %s", name, err)
				}
			}

			// nestedArgFields is the map used to parse the remaining fields: any field which isn't
			// part of ConnectionArgs should be a field of the args used for the paginated field.
			nestedArgFields := make(map[string]interface{})
			for name := range asMap {
				if _, ok := fields[name]; !ok {
					nestedArgFields[name] = asMap[name]
				}
			}

			if nestedArgParser == nil {
				if len(nestedArgFields) != 0 {
					return fmt.Errorf("error in parsing args")
				}
				return nil
			}

			fieldDest := dest.FieldByName("Args")
			tmpDest := reflect.New(nestedArgParser.Type)
			if err := nestedArgParser.FromJSON(nestedArgFields, tmpDest.Elem()); err != nil {
				return err
			}
			fieldDest.Set(tmpDest)

			return nil
		},
		Type: typ,
	}, argType, nil
}
