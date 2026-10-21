import { isCollection, parseDocument, visit, type Document } from "yaml";

/** One readable layout for generated configuration and the source editor. */
export function configYaml(doc: Document): string {
  visit(doc, { Node(_key, node) { if (isCollection(node)) node.flow = node.items.length === 0; } });
  return doc.toString({ indent: 2, lineWidth: 100 });
}

/** Keep invalid source intact so diagnostics and manual repairs remain possible. */
export function formatConfigSource(content: string): string {
  try {
    const doc = parseDocument(content, { intAsBigInt: true });
    return doc.errors.length > 0 ? content : configYaml(doc);
  } catch {
    return content;
  }
}
