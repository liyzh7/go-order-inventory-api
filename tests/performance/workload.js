import http from 'k6/http';
import exec from 'k6/execution';
import { check, sleep } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';

const manifest = JSON.parse(open(__ENV.PERF_MANIFEST));
const scenarioName = __ENV.PERF_SCENARIO || 'products-list-hot';
const baseURL = __ENV.BASE_URL || 'http://127.0.0.1:28081';
const runID = __ENV.RUN_ID || 'manual';
const requestTimeout = __ENV.REQUEST_TIMEOUT || '3s';

const businessSuccess = new Counter('business_success');
const businessRejected = new Counter('business_rejected');
const authFailures = new Counter('auth_failures');
const databaseFailures = new Counter('database_failures');
const idempotencyReplayed = new Counter('idempotency_replayed');
const cancellationReplayed = new Counter('cancellation_replayed');
const unexpectedResponses = new Counter('unexpected_responses');
const businessErrorRate = new Rate('business_error_rate');
const businessDuration = new Trend('business_duration', true);

function integer(name, fallback, minimum, maximum) {
  const parsed = Number.parseInt(__ENV[name] || String(fallback), 10);
  if (!Number.isInteger(parsed) || parsed < minimum || parsed > maximum) {
    throw new Error(`${name} must be between ${minimum} and ${maximum}`);
  }
  return parsed;
}

function executionOptions() {
  const model = __ENV.LOAD_MODEL || 'constant-vus';
  const vus = integer('VUS', 1, 1, 25);
  const duration = __ENV.DURATION || '5s';
  if (model === 'constant-arrival-rate') {
    return { executor: model, rate: integer('RATE', 10, 1, 200), timeUnit: '1s', duration,
      preAllocatedVUs: vus, maxVUs: 25, gracefulStop: '3s' };
  }
  if (model === 'shared-iterations') {
    return { executor: model, vus, iterations: integer('ITERATIONS', vus, 1, 10000), maxDuration: '30s' };
  }
  if (model !== 'constant-vus') {
    throw new Error('LOAD_MODEL must be constant-vus, constant-arrival-rate, or shared-iterations');
  }
  return { executor: model, vus, duration, gracefulStop: '3s' };
}

export const options = {
  scenarios: { benchmark: executionOptions() },
  discardResponseBodies: false,
  summaryTrendStats: ['avg', 'med', 'p(90)', 'p(95)', 'p(99)', 'min', 'max', 'count'],
  thresholds: {
    http_req_failed: ['rate<0.01'],
    business_error_rate: ['rate<0.01'],
    unexpected_responses: ['count==0'],
    http_req_duration: ['p(95)<1000'],
    // Saturation experiments must preserve dropped iterations as capacity
    // evidence instead of aborting before the runner can persist a result.
    dropped_iterations: [__ENV.ALLOW_DROPPED_ITERATIONS === 'true' ? 'count<100000' : 'count==0'],
  },
};

function requestParams(token, expectedStatuses = [200]) {
  const headers = token ? { Authorization: `Bearer ${token}` } : {};
  return { headers, timeout: requestTimeout, responseCallback: http.expectedStatuses(...expectedStatuses), tags: { workload: scenarioName } };
}

function parseErrorCode(response) {
  try { return response.json('error.code') || ''; } catch (_) { return ''; }
}

function record(response, acceptedStatuses, acceptedBusinessCodes = []) {
  businessDuration.add(response.timings.duration, { workload: scenarioName });
  const code = parseErrorCode(response);
  if (acceptedStatuses.includes(response.status)) {
    businessSuccess.add(1, { workload: scenarioName });
    businessErrorRate.add(false, { workload: scenarioName });
    return true;
  }
  if (acceptedBusinessCodes.includes(code)) {
    businessRejected.add(1, { reason: code, workload: scenarioName });
    businessErrorRate.add(false, { workload: scenarioName });
    return true;
  }
  if (response.status === 401) authFailures.add(1, { workload: scenarioName });
  if (response.status >= 500) databaseFailures.add(1, { workload: scenarioName });
  unexpectedResponses.add(1, { status: String(response.status), code, workload: scenarioName });
  businessErrorRate.add(true, { workload: scenarioName });
  return false;
}

function tokenFor(index) { return manifest.users[index % manifest.users.length].token; }
function productFor(index) { return 2 + (index % (manifest.product_count - 1)); }
function iteration() { return exec.scenario.iterationInTest; }

function productsList(hot) {
  const after = hot ? 0 : (iteration() * 37) % (manifest.product_count - 100);
  const limit = hot ? 20 : 100;
  const response = http.get(`${baseURL}/products?limit=${limit}&after_id=${after}`, requestParams('', [200]));
  const valid = record(response, [200]) && check(response, {
    'list envelope and pagination are present': (value) => Array.isArray(value.json('data.items')) && value.json('data.pagination') !== undefined,
  });
  if (!valid) businessErrorRate.add(true, { workload: scenarioName });
}

function productDetail(hot) {
  const productID = hot ? 2 : productFor(iteration() * 997 + __VU);
  const response = http.get(`${baseURL}/products/${productID}`, requestParams('', [200]));
  const valid = record(response, [200]) && check(response, { 'product id matches': (value) => value.json('data.id') === productID });
  if (!valid) businessErrorRate.add(true, { workload: scenarioName });
}

function createOrder(competition, idempotent, crossUser = false) {
  const userIndex = idempotent ? 0 : iteration() % manifest.users.length;
  // Concurrent idempotency must replay the same canonical business request;
  // varying a product with one key would correctly test conflict instead.
  const productID = competition ? manifest.competition_product_id : ((idempotent || crossUser) ? 2 : productFor(iteration() * 17 + __VU));
  const key = (idempotent || crossUser) ? `perf-idem-${runID}` : `perf-create-${runID}-${__VU}-${iteration()}`;
  const params = requestParams(tokenFor(userIndex), [200, 201, 409]);
  params.headers['Content-Type'] = 'application/json';
  params.headers['Idempotency-Key'] = key;
  const response = http.post(`${baseURL}/orders`, JSON.stringify({ items: [{ product_id: productID, quantity: 1 }] }), params);
  const ok = record(response, [200, 201], competition ? ['insufficient_stock'] : []);
  if (response.headers['Idempotency-Replayed'] === 'true') idempotencyReplayed.add(1, { workload: scenarioName });
  if (ok && (response.status === 200 || response.status === 201)) {
    check(response, { 'order response is complete': (value) => value.json('data.id') > 0 && value.json('data.items.0.product_id') === productID });
  }
}

function profileProduct(profile, index) {
  const availableProducts = manifest.product_count;
  if (profile === 'single-hotspot' || profile === 'limited-hotspot') return manifest.competition_product_id;
  if (profile === 'skewed-80-20') {
    const hotCount = Math.max(1, Math.floor(availableProducts * 0.20));
    if (index % 10 < 8) return 1 + ((index * 97 + __VU) % hotCount);
    const coldCount = availableProducts - hotCount;
    return hotCount + 1 + ((index * 997 + __VU) % coldCount);
  }
  return 1 + ((index * 997 + __VU * 17) % availableProducts);
}

function createProfileOrder(profile) {
  const index = iteration();
  const userIndex = index % manifest.users.length;
  const productID = profileProduct(profile, index);
  const params = requestParams(tokenFor(userIndex), [201, 409]);
  params.headers['Content-Type'] = 'application/json';
  // Keep the generated key safely below the API's 64-byte boundary even when
  // a warm-up suffix and four-digit iteration number are present.
  params.headers['Idempotency-Key'] = `p-${runID}-${__VU}-${index}`;
  const response = http.post(`${baseURL}/orders`, JSON.stringify({ items: [{ product_id: productID, quantity: 1 }] }), params);
  const acceptedRejections = profile === 'limited-hotspot' ? ['insufficient_stock'] : [];
  const ok = record(response, [201], acceptedRejections);
  if (ok && response.status === 201) {
    check(response, { 'profile order response is complete': (value) => value.json('data.id') > 0 && value.json('data.items.0.product_id') === productID });
  }
}

function seededOrder() {
  return manifest.cancellation_orders[iteration() % manifest.cancellation_orders.length];
}

function queryOrder() {
  const seeded = seededOrder();
  const response = http.get(`${baseURL}/orders/${seeded.id}`, requestParams(tokenFor(seeded.user_index), [200]));
  const valid = record(response, [200]) && check(response, { 'queried order id matches': (value) => value.json('data.id') === seeded.id });
  if (!valid) businessErrorRate.add(true, { workload: scenarioName });
}

function cancelOrder() {
  const seeded = seededOrder();
  const response = http.post(`${baseURL}/orders/${seeded.id}/cancel`, null, requestParams(tokenFor(seeded.user_index), [200]));
  const valid = record(response, [200]) && check(response, { 'cancelled state returned': (value) => value.json('data.status') === 'CANCELLED' });
  if (response.headers['Cancellation-Replayed'] === 'true') cancellationReplayed.add(1, { workload: scenarioName });
  if (!valid) businessErrorRate.add(true, { workload: scenarioName });
}

export default function () {
  switch (scenarioName) {
    case 'products-list-hot': productsList(true); break;
    case 'products-list-cursor': productsList(false); break;
    case 'product-detail-hot': productDetail(true); break;
    case 'product-detail-random': productDetail(false); break;
    case 'order-create': createOrder(false, false); break;
    case 'inventory-competition': createOrder(true, false); break;
    case 'idempotency-concurrent': createOrder(false, true); break;
    case 'idempotency-cross-user': createOrder(false, false, true); break;
    case 'order-query': queryOrder(); break;
    case 'order-cancel': cancelOrder(); break;
    case 'order-create-distributed': createProfileOrder('distributed'); break;
    case 'order-create-skewed': createProfileOrder('skewed-80-20'); break;
    case 'order-create-hotspot': createProfileOrder('single-hotspot'); break;
    case 'order-create-hotspot-limited': createProfileOrder('limited-hotspot'); break;
    default: throw new Error(`unsupported PERF_SCENARIO ${scenarioName}`);
  }
  if ((__ENV.ITERATION_SLEEP_MS || '0') !== '0') sleep(Number(__ENV.ITERATION_SLEEP_MS) / 1000);
}
