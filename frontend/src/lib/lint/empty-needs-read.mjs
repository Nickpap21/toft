/**
 * toft/empty-needs-read: a failed read must not be drawn as an answer.
 *
 * Reports a useQuery, useInfiniteQuery or useQueries result whose data, or a
 * variable derived from it, is tested for emptiness or absence, when nothing
 * reads that query's failure. "Nothing needs you" over a read that failed is
 * a false claim, and a skeleton over one never ends.
 *
 * A claim is a test of emptiness, an all-clear from .every or .some, a count
 * that was defaulted and is printed or compared, or text drawn in place of the
 * data. A failure read counts only where it guards the claim: in front of it,
 * in the same condition, or in the tag of an element it sits in. A claim from
 * a defaulted value (`data ?? []`) is also false while the read is going: it
 * needs a test of the data, or of isLoading or isPending, too.
 *
 * The fix is <Read query={q} ...> (components/shell/unreachable), or testing
 * readFailed(q), isError, error, isLoadingError, status or failureReason in
 * front of the claim. "—" isn't a claim: it says the answer isn't known. A
 * read whose failure draws no claim at all (the options of a filter menu, a
 * decoration) says why in its options:
 *
 *   useQuery({ ..., meta: { quietFailure: "<why>" } })
 *
 * That is the only way out. An eslint-disable is not the way out: it hides
 * the reason from the next reader.
 *
 * Follows what one function shows: a read behind a custom hook isn't tracked.
 */

const HOOKS = new Set(["useQuery", "useInfiniteQuery", "useQueries"]);
const FAILURE = new Set(["isError", "error", "isLoadingError", "isSuccess", "status", "failureReason", "errorUpdateCount"]);
const LOADING = new Set(["isLoading", "isPending", "isSuccess", "status"]);
/** Drawn in place of data, these say it isn't known: "—", " ", "— services". */
const unknown = (s) => !/\p{L}/u.test(s) || /^\s*[—–…]/.test(s);
const WRAPPERS = new Set(["ChainExpression", "TSNonNullExpression", "TSAsExpression", "TSSatisfiesExpression"]);
const TESTING = new Set(["LogicalExpression", "UnaryExpression", "BinaryExpression"]);
const FLIP = { ">": "<", "<": ">", ">=": "<=", "<=": ">=" };
const EMPTINESS = new Set(["=== 0", "== 0", "!== 0", "!= 0", "> 0", "<= 0", ">= 1", "< 1"]);

const prop = (obj, name) =>
  obj?.type === "ObjectExpression" ? obj.properties.find((p) => p.type === "Property" && (p.key.name ?? p.key.value) === name)?.value : undefined;

/** The options' own reason for a failure that draws no claim. */
function hasReason(options) {
  const why = prop(prop(options, "meta"), "quietFailure");
  return typeof why?.value === "string" && why.value.trim() !== "";
}

function isQuiet(call) {
  const [options] = call.arguments;
  if (call.callee.name !== "useQueries") return hasReason(options);
  const queries = prop(options, "queries");
  const each = queries?.type === "ArrayExpression" ? queries.elements : [queries?.arguments?.[0]?.body];
  return each.length > 0 && each.every(hasReason);
}

const saysUnknown = (el) => el.children.length === 1 && el.children[0].type === "JSXText" && unknown(el.children[0].value);

const isJSX = (n) =>
  (n?.type === "JSXElement" && !saysUnknown(n)) ||
  n?.type === "JSXFragment" ||
  (n?.type === "ConditionalExpression" && (isJSX(n.consequent) || isJSX(n.alternate))) ||
  (n?.type === "LogicalExpression" && isJSX(n.right));

const renders = (n) => isJSX(n) || (n?.type === "Literal" && n.value === null);

const isText = (n) =>
  n?.type === "TemplateLiteral" ||
  (n?.type === "Literal" && typeof n.value === "string" && !unknown(n.value)) ||
  (n?.type === "ConditionalExpression" && isText(n.consequent) && isText(n.alternate));

/** Text on the page: not an attribute, nor a word inside a sentence's template. */
function inMarkup(n) {
  for (; n; n = n.parent) {
    if (n.type === "JSXExpressionContainer") return n.parent.type !== "JSXAttribute";
    if (/^(TemplateLiteral|ArrayExpression|CallExpression)$|Statement$|Function/.test(n.type)) return false;
  }
  return false;
}

/** The word on a button or a link says what it does, not what the read found. */
function onButton(n) {
  for (; n; n = n.parent) {
    if (n.type === "JSXElement" && /^(button|Link)$/i.test(n.openingElement.name.name ?? "")) return true;
    if (/Statement$|Function/.test(n.type)) return false;
  }
  return false;
}

/** enabled: choosing whether to read, drawing nothing. */
function inEnabled(n) {
  for (; n; n = n.parent) {
    if (n.type === "Property") return n.key.name === "enabled";
    if (/Statement$|Function|^JSX/.test(n.type)) return false;
  }
  return false;
}

const returning = (stmt, test) =>
  stmt?.type === "BlockStatement" ? stmt.body.some((s) => returning(s, test)) : stmt?.type === "ReturnStatement" && test(stmt.argument);

function comparesEmpty({ operator, left, right }, node) {
  const value = left === node ? right : left;
  const op = left === node ? operator : (FLIP[operator] ?? operator);
  return value.type === "Literal" && EMPTINESS.has(`${op} ${value.value}`);
}

function inPredicate(n) {
  while (n.parent.type === "LogicalExpression" || n.parent.type === "UnaryExpression") n = n.parent;
  return n.parent.type === "ArrowFunctionExpression";
}

const within = (inner, outer) => inner.range[0] >= outer.range[0] && inner.range[1] <= outer.range[1];

/** Tested with something else: `isError && !data ? … : claim` reaches the claim with no data. */
function conjoined(g, root) {
  for (let n = g; n !== root; n = n.parent) if (n.parent.type === "LogicalExpression" && n.parent.operator === "&&") return true;
  return false;
}

/** `g` decides whether the claim `t` is reached. `alone`: only if it does so
 *  by itself, as a test of the data must. */
function guards(g, t, alone = false) {
  if (g === t) return false;
  for (let n = t, p = n.parent; p; n = p, p = p.parent) {
    if (p.type === "ConditionalExpression" || p.type === "IfStatement") {
      if (n === p.test ? within(g, p) : within(g, p.test) && !(alone && n !== p.consequent && conjoined(g, p.test))) return true;
    } else if (p.type === "LogicalExpression") {
      if (within(g, p)) return true;
    } else if (p.type === "JSXElement") {
      if (within(g, p.openingElement)) return true;
    } else if (p.type === "BlockStatement" || p.type === "Program") {
      for (const s of p.body) {
        if (s === n) break;
        if (s.type === "IfStatement" && within(g, s.test) && returning(s.consequent, () => true) && !(alone && conjoined(g, s.test))) return true;
      }
    }
  }
  return false;
}

/** `x.length > 0 && <Row />`: with no data it draws nothing, so it says nothing. */
function drawsOnlySome(node, defaulted) {
  if (node.callee?.property?.name === "every") return false; // True of an empty list.
  let whole = node;
  if (whole.parent.type === "UnaryExpression" && whole.parent.parent.type === "UnaryExpression") whole = whole.parent.parent; // !!x
  const { parent } = whole;
  if (parent.type === "UnaryExpression") return false;
  if (parent.type === "BinaryExpression") {
    if (!comparesEmpty(parent, whole)) return false;
    const op = parent.left === whole ? parent.operator : (FLIP[parent.operator] ?? parent.operator);
    if (defaulted && ["===", "==", "<=", "<"].includes(op)) return false; // x?.length === 0 is false with no data; a defaulted one's is true.
    whole = parent;
  }
  const gate = whole.parent;
  if (gate.type === "LogicalExpression") return gate.operator !== "??";
  return gate.type === "ConditionalExpression" && gate.test === whole && (gate.alternate.name === "undefined" || (gate.alternate.type === "Literal" && (gate.alternate.value === null || gate.alternate.value === "")));
}

/** Decides what's drawn: not a validation, a handler, a button's word or a
 *  disabled button. `assigned`: a name made from the count, or a test of one. */
function drawing(node, assigned) {
  let test = node;
  while (TESTING.has(test.parent.type) || WRAPPERS.has(test.parent.type)) test = test.parent;
  const p = test.parent;
  if (p.type === "IfStatement" && p.test === test) return returning(p.consequent, renders) || returning(p.alternate, renders);
  const choosing = p.type === "ConditionalExpression" && p.test === test;
  if (choosing && onButton(node)) return false;
  if (!assigned) return !choosing || inMarkup(p) || isJSX(p.consequent) || isJSX(p.alternate) || drawsOnlySome(node, false); // Its printed count, below, needs it.
  if (test.type === "LogicalExpression" && /^(button|Link)$/i.test(test.right.openingElement?.name.name ?? "")) return false;
  if (p.type === "JSXExpressionContainer" && p.parent.type === "JSXAttribute") return false;
  return !choosing || [p.consequent, p.alternate].some((b) => renders(b) || isText(b));
}

/** @type {import("eslint").Rule.RuleModule} */
const rule = {
  meta: {
    type: "problem",
    docs: { description: "A failed query must not be drawn as an empty list or a skeleton that never ends." },
    schema: [],
    messages: {
      unread:
        "If this read fails it is drawn as an answer: nothing reads its failure. Use <Read query={…}>, read isError or error, or, if its failure draws no claim, say why with meta: { quietFailure: \"…\" }.",
    },
  },

  create(context) {
    const { sourceCode } = context;

    function variableOf(id) {
      for (let scope = sourceCode.getScope(id); scope; scope = scope.upper) {
        const found = scope.set.get(id.name);
        if (found) return found;
      }
      return undefined;
    }

    const isReadQuery = (n) => n.parent.type === "JSXExpressionContainer" && n.parent.parent.name?.name === "query" && n.parent.parent.parent.name?.name === "Read";

    /** Records where a query's failure or loading state is read: there, and
     *  at every use of a name it is assigned to. */
    function mark(list, node, q) {
      list.push(node);
      if (list === q.reads && isReadQuery(node)) q.present.push(node); // Read's children run only with data.
      for (let n = node; n.parent; n = n.parent) {
        const p = n.parent;
        if (p.type === "VariableDeclarator" && p.init === n) return markName(list, p.id, q);
        if (/Statement$|^JSX|^Program$/.test(p.type)) return;
      }
    }

    function markName(list, id, q) {
      if (id.type !== "Identifier") return;
      for (const ref of variableOf(id)?.references ?? []) if (ref.isRead()) mark(list, ref.identifier, q);
    }

    function claim(node, q, defaulted, absent = false) {
      // const empty = rows.length === 0: the claim is where the name is used.
      let whole = node;
      while (TESTING.has(whole.parent.type) || WRAPPERS.has(whole.parent.type)) whole = whole.parent;
      const { parent } = whole;
      if (!absent && parent.type === "VariableDeclarator" && parent.init === whole && parent.id.type === "Identifier") {
        for (const ref of variableOf(parent.id)?.references ?? []) if (ref.isRead()) tested(ref.identifier, q, defaulted, true);
        return;
      }
      q.tests.push({ node, defaulted, absent, blank: !absent && drawsOnlySome(node, defaulted) });
    }

    /** Stays "defaulted" once a value is: it's what makes a zero a claim.
     *  ("total": a field of the data itself, as an API's own count is.) */
    const next = (kind) => (kind === "defaulted" ? kind : "derived");

    /** Follows every read of a name bound to a query, its data or a value derived from them. */
    function bind(pattern, kind, q) {
      switch (pattern.type) {
        case "Identifier":
          for (const ref of variableOf(pattern)?.references ?? []) if (ref.isRead()) follow(ref.identifier, kind, q);
          break;
        case "AssignmentPattern": // { data: rows = [] }: never absent, still empty.
          bind(pattern.left, "defaulted", q);
          break;
        case "RestElement":
          bind(pattern.argument, kind, q);
          break;
        case "ArrayPattern":
          for (const el of pattern.elements) if (el) bind(el, kind === "queries" ? "query" : next(kind), q);
          break;
        case "ObjectPattern":
          for (const p of pattern.properties) {
            if (p.type === "RestElement") bind(p.argument, kind, q);
            else if (kind !== "query") bind(p.value, next(kind), q);
            else if (p.key.name === "data") bind(p.value, "data", q);
            else {
              if (FAILURE.has(p.key.name)) markName(q.reads, p.value, q);
              if (LOADING.has(p.key.name)) markName(q.loads, p.value, q);
            }
          }
          break;
      }
    }

    function member(node, kind, q) {
      const name = node.computed ? null : node.property.name;
      const call = node.parent.type === "CallExpression" && node.parent.callee === node ? node.parent : null;
      if (kind === "queries") {
        if (node.computed) return follow(node, "query", q);
        if (!call) return;
        const [arg] = call.arguments;
        if (arg?.name === "readFailed") mark(q.reads, call, q); // queries.some(readFailed)
        const param = arg?.params?.[0]; // results.some((r) => r.isError)
        if (param) bind(param, "query", q);
        follow(call, "derived", q);
      } else if (kind === "query") {
        if (FAILURE.has(name)) mark(q.reads, node, q);
        if (LOADING.has(name)) mark(q.loads, node, q);
        if (name === "data") follow(node, "data", q);
      } else if (name === "length" || name === "size" || (call && (name === "some" || name === "every"))) {
        tested(call ?? node, q, kind === "defaulted");
      } else {
        follow(call ?? node, kind === "data" && !call ? "total" : next(kind), q);
      }
    }

    /** A .length, .size, .some or .every, or a count assigned from one:
     *  reported when it's asked whether there is any, or a defaulted one is
     *  printed. `assigned`: it's a name, whose every comparison is a claim. */
    function tested(node, q, defaulted, assigned = false) {
      // (x?.length ?? 0) > 0 asks the same as x?.length > 0.
      while (WRAPPERS.has(node.parent.type) || (node.parent.type === "LogicalExpression" && node.parent.operator === "??" && node.parent.left === node)) {
        node = node.parent;
        if (node.type === "LogicalExpression") defaulted ||= !(typeof node.right.value === "string" && unknown(node.right.value));
      }
      // rows.filter((r) => !others.some(…)): choosing rows, drawing nothing.
      if ((node.type === "CallExpression" && inPredicate(node)) || inEnabled(node)) return;
      const p = node.parent;
      if (assigned && p.type === "BinaryExpression" && (p.left.name === "undefined" || p.right.name === "undefined")) return void q.present.push(p);
      if (!drawing(node, assigned)) return;
      if (
        (p.type === "UnaryExpression" && p.operator === "!") ||
        (p.type === "BinaryExpression" && (comparesEmpty(p, node) || (assigned && ["<", "<=", ">", ">=", "===", "!=="].includes(p.operator)))) ||
        (["IfStatement", "ConditionalExpression", "WhileStatement"].includes(p.type) && p.test === node) ||
        (p.type === "LogicalExpression" && p.operator !== "??") ||
        (defaulted && !onButton(node) && (p.type === "TemplateLiteral" || (p.type === "JSXExpressionContainer" && p.parent.type !== "JSXAttribute")))
      )
        claim(node, q, defaulted);
      else if (p.type === "VariableDeclarator" && p.init === node && p.id.type === "Identifier")
        for (const ref of variableOf(p.id)?.references ?? []) if (ref.isRead()) tested(ref.identifier, q, defaulted, true);
    }

    /** The data itself tested: reported when the test decides what renders
     *  for a read with no data. `absent`: the test is true when it's absent. */
    function absence(node, absent, q, origin) {
      const p = node.parent;
      if (WRAPPERS.has(p.type)) return absence(p, absent, q, origin);
      if (p.type === "UnaryExpression" && p.operator === "!") return absence(p, !absent, q, origin);
      if (p.type === "VariableDeclarator" && p.init === node) return markName(q.present, p.id, q); // const ready = !!data
      const text = inMarkup(p) && !onButton(p) ? isText : () => false;
      const draws = (n) => (absent ? renders(n) : isJSX(n)) || text(n); // `data ? <X /> : null` is `data && <X />`
      if (p.type === "IfStatement" && p.test === node) {
        q.present.push(origin);
        if (returning(absent ? p.consequent : p.alternate, draws)) claim(origin, q, false, true);
      } else if (p.type === "ConditionalExpression" && p.test === node) {
        q.present.push(origin);
        if (draws(absent ? p.consequent : p.alternate)) claim(origin, q, false, true);
      } else if (p.type === "LogicalExpression" && p.operator !== "??") {
        if (p.operator === "||" && p.left === node && /^(ArrayExpression|ObjectExpression|Literal)$/.test(p.right.type)) return; // data || []
        q.present.push(origin);
        if (p.operator === "&&" && p.left === node && absent && (isJSX(p.right) || text(p.right))) claim(origin, q, false, true);
        else absence(p, absent, q, origin);
      }
    }

    /** Climbs from one read of a query, its data or a derived value. */
    function follow(node, kind, q) {
      const p = node.parent;
      if (WRAPPERS.has(p.type)) return follow(p, kind, q);
      if (p.type === "MemberExpression" && p.object === node) return member(p, kind, q);
      if (p.type === "VariableDeclarator" && p.init === node) return bind(p.id, kind, q);
      if (p.type === "ArrayExpression" && kind === "query") return follow(p, "queries", q);
      if (kind === "query" && p.type === "CallExpression" && (p.callee.name === "readFailed" || p.callee.name === "allRead")) return mark(q.reads, p, q);
      if (p.type === "JSXExpressionContainer") {
        if (kind === "query" && isReadQuery(node)) mark(q.reads, node, q);
        // <List rows={data ?? []} />: the child can't tell it from an answer.
        else if (p.parent.type === "JSXAttribute" && node.type === "LogicalExpression" && (node.right.elements?.length === 0 || node.right.properties?.length === 0)) q.tests.push({ node, handed: true });
        return;
      }
      if (kind === "query" || kind === "queries") return;
      if (p.type === "LogicalExpression" && (p.operator === "??" || p.operator === "||")) follow(p, "defaulted", q);
      if ((p.type === "CallExpression" || p.type === "NewExpression") && p.arguments.includes(node)) return follow(p, next(kind), q);
      if (p.type === "ArrowFunctionExpression" && p.body === node && p.parent.type === "CallExpression" && p.parent.callee.name === "useMemo") return follow(p.parent, next(kind), q);
      if (kind === "total" && p.type === "BinaryExpression" && comparesEmpty(p, node)) return claim(node, q, false); // data?.total === 0
      if (kind !== "data") return;
      if (p.type === "BinaryExpression" && ["===", "==", "!==", "!="].includes(p.operator)) {
        const other = p.left === node ? p.right : p.left;
        if (other.name === "undefined" || other.raw === "null") absence(p, p.operator.startsWith("="), q, p);
        return;
      }
      absence(node, false, q, node);
    }

    return {
      CallExpression(node) {
        if (node.callee.type !== "Identifier" || !HOOKS.has(node.callee.name)) return;
        const q = { reads: [], loads: [], present: [], tests: [] };
        follow(node, node.callee.name === "useQueries" ? "queries" : "query", q);
        const guarded = (list, t, alone) => list.some((g) => guards(g, t, alone));
        const open = q.tests.filter(({ node: t, defaulted, absent, blank, handed }) => {
          if (!absent && q.present.includes(t)) return false; // A name that says the data is in.
          if (handed) return guarded(q.loads, t) && !guarded(q.reads, t); // Loading handled, failure not.
          if (absent) return !guarded(q.reads, t);
          if (q.tests.some((o) => o.blank && guards(o.node, t))) return false; // Printed only when there are some.
          if (blank && q.reads.length > 0) return false;
          if (guarded(q.present, t, true)) return false;
          return !guarded(q.reads, t) || (defaulted && !guarded(q.loads, t));
        });
        if (open.length === 0 || isQuiet(node)) return;
        context.report({ node: open.map((t) => t.node).reduce((a, b) => (b.range[0] < a.range[0] ? b : a)), messageId: "unread" });
      },
    };
  },
};

export default rule;
