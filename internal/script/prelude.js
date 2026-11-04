// A chai-style expect covering what Bruno tests and assertions use, plus test() and the assertion operators.
(function (g) {
  const inspect = (v) => {
    if (typeof v === 'string') return JSON.stringify(v);
    if (v === undefined) return 'undefined';
    try { return JSON.stringify(v); } catch (e) { return String(v); }
  };
  const typeOf = (v) => (v === null ? 'null' : Array.isArray(v) ? 'array' : v instanceof Date ? 'date' : v instanceof RegExp ? 'regexp' : typeof v);
  const deepEqual = (a, b) => {
    if (a === b || (Number.isNaN(a) && Number.isNaN(b))) return true;
    if (typeOf(a) !== typeOf(b) || typeof a !== 'object' || a === null) return false;
    if (a instanceof Date) return a.getTime() === b.getTime();
    const ka = Object.keys(a), kb = Object.keys(b);
    return ka.length === kb.length && ka.every((k) => deepEqual(a[k], b[k]));
  };
  const lengthOf = (v) => (v == null ? undefined : typeof v.length === 'number' ? v.length : typeof v === 'object' ? Object.keys(v).length : undefined);
  // Like chai: strict equality unless .deep.
  const includes = (hay, needle, deep) => {
    const same = deep ? deepEqual : (a, b) => a === b || (Number.isNaN(a) && Number.isNaN(b));
    if (typeof hay === 'string') return hay.indexOf(String(needle)) >= 0;
    if (Array.isArray(hay)) return hay.some((x) => same(x, needle));
    if (hay && typeof hay === 'object' && needle && typeof needle === 'object') {
      return Object.keys(needle).every((k) => same(hay[k], needle[k]));
    }
    return false;
  };

  function Assertion(obj, negate) {
    this._obj = obj;
    this._not = !!negate;
  }
  const P = Assertion.prototype;
  P.check = function (ok, msg, notMsg) {
    if (this._not ? ok : !ok) throw new Error(this._not ? notMsg : msg);
    return this;
  };
  ['to', 'be', 'been', 'is', 'that', 'which', 'and', 'has', 'have', 'with', 'at', 'of', 'same', 'does'].forEach((w) =>
    Object.defineProperty(P, w, { get() { return this; } })
  );
  Object.defineProperty(P, 'not', { get() { this._not = !this._not; return this; } });
  Object.defineProperty(P, 'deep', { get() { this._deep = true; return this; } });
  const getters = {
    ok: (v) => !!v, true: (v) => v === true, false: (v) => v === false, null: (v) => v === null,
    undefined: (v) => v === undefined, exist: (v) => v != null, NaN: (v) => Number.isNaN(v),
    json: (v) => typeOf(v) === 'object' || typeOf(v) === 'array',
  };
  // Like chai, .empty only applies to strings, arrays and objects; anything else fails, negated or not.
  Object.defineProperty(P, 'empty', {
    get() {
      const v = this._obj;
      if (typeof v !== 'string' && (v === null || typeof v !== 'object')) {
        throw new Error(`.empty was passed non-string primitive ${inspect(v)}`);
      }
      const n = typeof v === 'string' || Array.isArray(v) ? v.length : Object.keys(v).length;
      return this.check(n === 0, `expected ${inspect(v)} to be empty`, `expected ${inspect(v)} not to be empty`);
    },
  });
  Object.keys(getters).forEach((name) =>
    Object.defineProperty(P, name, {
      get() { return this.check(getters[name](this._obj), `expected ${inspect(this._obj)} to be ${name}`, `expected ${inspect(this._obj)} not to be ${name}`); },
    })
  );
  P.equal = P.equals = P.eq = function (v) {
    if (this._deep) return this.eql(v);
    return this.check(this._obj === v, `expected ${inspect(this._obj)} to equal ${inspect(v)}`, `expected ${inspect(this._obj)} to not equal ${inspect(v)}`);
  };
  P.eql = function (v) {
    return this.check(deepEqual(this._obj, v), `expected ${inspect(this._obj)} to deeply equal ${inspect(v)}`, `expected ${inspect(this._obj)} to not deeply equal ${inspect(v)}`);
  };
  P.a = P.an = function (type) {
    return this.check(typeOf(this._obj) === String(type).toLowerCase(), `expected ${inspect(this._obj)} to be a ${type}`, `expected ${inspect(this._obj)} not to be a ${type}`);
  };
  P.include = P.includes = P.contain = P.contains = function (v) {
    return this.check(includes(this._obj, v, this._deep), `expected ${inspect(this._obj)} to include ${inspect(v)}`, `expected ${inspect(this._obj)} to not include ${inspect(v)}`);
  };
  P.property = function (name, value) {
    const has = this._obj != null && Object.prototype.hasOwnProperty.call(Object(this._obj), name);
    if (arguments.length < 2) {
      this.check(has, `expected ${inspect(this._obj)} to have property ${inspect(name)}`, `expected ${inspect(this._obj)} to not have property ${inspect(name)}`);
      return has && !this._not ? new Assertion(this._obj[name]) : this;
    }
    const same = this._deep ? deepEqual(this._obj[name], value) : this._obj[name] === value;
    return this.check(has && same, `expected property ${inspect(name)} to equal ${inspect(value)}`, `expected property ${inspect(name)} to not equal ${inspect(value)}`);
  };
  P.lengthOf = P.length = function (n) {
    return this.check(lengthOf(this._obj) === n, `expected ${inspect(this._obj)} to have length ${n}`, `expected ${inspect(this._obj)} to not have length ${n}`);
  };
  const cmp = (name, f) => function (n) {
    if (typeof this._obj !== 'number' && !(this._obj instanceof Date)) {
      throw new Error(`expected ${inspect(this._obj)} to be a number or a date`);
    }
    return this.check(f(this._obj, n), `expected ${inspect(this._obj)} to be ${name} ${inspect(n)}`, `expected ${inspect(this._obj)} not to be ${name} ${inspect(n)}`);
  };
  P.above = P.gt = P.greaterThan = cmp('above', (a, b) => a > b);
  P.least = P.gte = P.greaterThanOrEqual = cmp('at least', (a, b) => a >= b);
  P.below = P.lt = P.lessThan = cmp('below', (a, b) => a < b);
  P.most = P.lte = P.lessThanOrEqual = cmp('at most', (a, b) => a <= b);
  P.within = function (lo, hi) {
    return this.check(this._obj >= lo && this._obj <= hi, `expected ${inspect(this._obj)} to be within ${lo}..${hi}`, `expected ${inspect(this._obj)} not to be within ${lo}..${hi}`);
  };
  P.oneOf = function (list) {
    return this.check(list.some((x) => x === this._obj), `expected ${inspect(this._obj)} to be one of ${inspect(list)}`, `expected ${inspect(this._obj)} not to be one of ${inspect(list)}`);
  };
  P.match = function (re) {
    return this.check(this._obj !== undefined && re.test(this._obj), `expected ${inspect(this._obj)} to match ${re}`, `expected ${inspect(this._obj)} not to match ${re}`);
  };
  P.startWith = P.startsWith = function (s) {
    return this.check(typeof this._obj === 'string' && this._obj.startsWith(s), `expected ${inspect(this._obj)} to start with ${inspect(s)}`, `expected ${inspect(this._obj)} not to start with ${inspect(s)}`);
  };
  P.endWith = P.endsWith = function (s) {
    return this.check(typeof this._obj === 'string' && this._obj.endsWith(s), `expected ${inspect(this._obj)} to end with ${inspect(s)}`, `expected ${inspect(this._obj)} not to end with ${inspect(s)}`);
  };
  P.satisfy = function (f) {
    return this.check(!!f(this._obj), `expected ${inspect(this._obj)} to satisfy the predicate`, `expected ${inspect(this._obj)} not to satisfy the predicate`);
  };

  g.expect = (v) => new Assertion(v);
  // __show renders a console.log argument: Errors as text, functions by name, cycles marked.
  g.__show = (v) => {
    if (typeof v === 'string') return v;
    if (v === undefined) return 'undefined';
    if (typeof v === 'function') return '[Function]';
    if (v instanceof Error) return String(v);
    const seen = [];
    try {
      return JSON.stringify(v, function (k, x) {
        if (typeof x === 'function') return '[Function]';
        if (x instanceof Error) return String(x);
        if (x && typeof x === 'object') {
          if (seen.indexOf(x) >= 0) return '[Circular]';
          seen.push(x);
        }
        return x;
      });
    } catch (e) {
      return String(v);
    }
  };
  g.__tests = [];
  g.test = (name, fn) => {
    const done = (e) => g.__tests.push(e === undefined
      ? { name: String(name), passed: true }
      : { name: String(name), passed: false, error: String((e && e.message) || e) });
    try {
      const r = fn();
      if (r && typeof r.then === 'function') return r.then(() => done(), (e) => done(e === undefined ? 'rejected' : e));
      done();
    } catch (e) {
      done(e === undefined ? 'threw undefined' : e);
    }
  };

  // __assert applies one Bruno assertion row: the same operator table as Bruno's assert runtime.
  g.__assert = (lhs, op, rhs) => {
    const e = expect(lhs);
    switch (op) {
      case 'eq': return e.to.equal(rhs);
      case 'neq': return e.to.not.equal(rhs);
      case 'gt': return e.to.be.greaterThan(rhs);
      case 'gte': return e.to.be.greaterThanOrEqual(rhs);
      case 'lt': return e.to.be.lessThan(rhs);
      case 'lte': return e.to.be.lessThanOrEqual(rhs);
      case 'in': return e.to.be.oneOf(rhs);
      case 'notIn': return e.to.not.be.oneOf(rhs);
      case 'contains': return e.to.include(rhs);
      case 'notContains': return e.to.not.include(rhs);
      case 'length': return e.to.have.lengthOf(rhs);
      case 'matches': return e.to.match(new RegExp(rhs));
      case 'notMatches': return e.to.not.match(new RegExp(rhs));
      case 'startsWith': return e.to.startWith(rhs);
      case 'endsWith': return e.to.endWith(rhs);
      case 'between': return e.to.be.within(rhs[0], rhs[1]);
      case 'isEmpty': return e.to.be.empty;
      case 'isNotEmpty': return e.to.not.be.empty;
      case 'isNull': return e.to.be.null;
      case 'isUndefined': return e.to.be.undefined;
      case 'isDefined': return e.to.not.be.undefined;
      case 'isTruthy': return e.to.be.true;
      case 'isFalsy': return e.to.be.false;
      case 'isJson': return e.to.be.json;
      case 'isNumber': return e.to.be.a('number');
      case 'isString': return e.to.be.a('string');
      case 'isBoolean': return e.to.be.a('boolean');
      case 'isArray': return e.to.be.a('array');
      default: return e.to.equal(rhs);
    }
  };
})(globalThis);
