// Publishes a caret the way an editor built on @slate-yjs/core does,
// so the Go tests can read what the real library writes into awareness
// rather than what they would have written themselves.
//
// One JSON object on stdin, one on stdout. In:
//
//   {
//     clientId, update,              the Yjs client to be, and the
//                                    document to join, as a base64 v1 update
//     block, appId, field,           the content block whose editable
//                                    field the editor is bound to
//     anchor: {paragraph, offset},   the selection, as Slate points into
//     focus:  {paragraph, offset},   the field's paragraphs
//     data                           what to publish beside the caret
//   }
//
// Out: {clientId, update, state, children, docChanged} - the awareness
// update carrying this client's state as base64, the state itself as
// the JSON string on the wire, the Slate tree the editor built from the
// field, and whether binding the editor wrote anything back to the
// document, which it does when the field is not a shape it accepts.

import { createEditor } from 'slate'
import * as Y from 'yjs'
import { Awareness, encodeAwarenessUpdate } from 'y-protocols/awareness.js'
import { withCursors, withYjs, CursorEditor, YjsEditor } from '@slate-yjs/core'
import { Buffer } from 'node:buffer'

async function readStdin() {
  const chunks = []
  for await (const chunk of process.stdin) chunks.push(chunk)
  return Buffer.concat(chunks).toString('utf8')
}

const msg = JSON.parse(await readStdin())

const doc = new Y.Doc()
doc.clientID = msg.clientId
Y.applyUpdate(doc, new Uint8Array(Buffer.from(msg.update, 'base64')))

const block = doc.getMap('document').get('content').get(msg.block)
const field = block.get('_collab').get(msg.appId).get(msg.field)
if (!(field instanceof Y.XmlText)) {
  throw new Error(`block ${msg.block} holds no Y.XmlText under ${msg.appId}/${msg.field}`)
}

const before = Y.encodeStateVector(doc)

const awareness = new Awareness(doc)
const editor = withCursors(
  withYjs(createEditor(), field),
  awareness,
  { data: msg.data },
)

YjsEditor.connect(editor)

// Slate reports the operations its normalizer produced through
// onChange, which it schedules rather than calls, and slate-yjs flushes
// them to the document from there. Let that happen before deciding
// whether the document changed.
await new Promise((resolve) => setTimeout(resolve, 0))
YjsEditor.flushLocalChanges(editor)

const point = (p) => ({ path: [p.paragraph, 0], offset: p.offset })

CursorEditor.sendCursorPosition(editor, {
  anchor: point(msg.anchor),
  focus: point(msg.focus),
})

const after = Y.encodeStateVector(doc)

process.stdout.write(JSON.stringify({
  clientId: doc.clientID,
  update: Buffer.from(encodeAwarenessUpdate(awareness, [doc.clientID])).toString('base64'),
  state: JSON.stringify(awareness.getLocalState()),
  children: editor.children,
  docChanged: Buffer.compare(Buffer.from(before), Buffer.from(after)) !== 0,
}) + '\n')

// Awareness keeps a timer for outdated peers; without this the process
// never exits.
awareness.destroy()
doc.destroy()
