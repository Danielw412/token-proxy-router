import { parseDocument } from 'yaml';

export interface BasicSettings {
  port: number;
  debug: boolean;
  loggingToFile: boolean;
  requestLog: boolean;
  routingStrategy: string;
  sessionAffinity: boolean;
  requestRetry: number;
}

const toNumber = (value: unknown, fallback: number): number =>
  typeof value === 'number' && Number.isFinite(value) ? value : fallback;

const PORT = ['server', 'port'];
const LOGS = ['observability', 'logs'];
const RETRY = ['routing', 'retry', 'request-retry'];

/** The essentials, read from the same v8 YAML document the advanced editor writes. */
export const readBasicSettings = (yaml: string): BasicSettings => {
  const doc = parseDocument(yaml);
  const strategy = doc.getIn(['routing', 'strategy']);
  return {
    port: toNumber(doc.getIn(PORT), 8317),
    debug: doc.getIn([...LOGS, 'debug']) === true,
    loggingToFile: doc.getIn([...LOGS, 'logging-to-file']) === true,
    requestLog: doc.getIn([...LOGS, 'request-log']) === true,
    routingStrategy: typeof strategy === 'string' ? strategy : 'round-robin',
    sessionAffinity: doc.getIn(['routing', 'session-affinity']) === true,
    requestRetry: toNumber(doc.getIn(RETRY), 3),
  };
};

export const writeBasicSettings = (yaml: string, next: BasicSettings): string => {
  const doc = parseDocument(yaml);
  doc.setIn(PORT, next.port);
  doc.setIn([...LOGS, 'debug'], next.debug);
  doc.setIn([...LOGS, 'logging-to-file'], next.loggingToFile);
  doc.setIn([...LOGS, 'request-log'], next.requestLog);
  doc.setIn(['routing', 'strategy'], next.routingStrategy);
  doc.setIn(['routing', 'session-affinity'], next.sessionAffinity);
  doc.setIn(RETRY, next.requestRetry);
  return doc.toString();
};
