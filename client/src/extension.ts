import * as fs from 'fs';
import * as path from 'path';
import { ExtensionContext, Uri, Position, Range, DocumentLink, DocumentLinkProvider, TextDocument, CancellationToken, commands, window, workspace, languages, MarkdownString, Hover } from 'vscode';

import {
  LanguageClient,
  LanguageClientOptions,
  RevealOutputChannelOn,
  ServerOptions,
  TransportKind
} from 'vscode-languageclient/node';
import { K8sFileSystemProvider } from './virtualDocumentProvider';

let client: LanguageClient;

class SubPathDocumentLinkProvider implements DocumentLinkProvider {
  provideDocumentLinks(document: TextDocument, _token: CancellationToken): DocumentLink[] {
    const links: DocumentLink[] = [];

    for (let line = 0; line < document.lineCount; line++) {
      const textLine = document.lineAt(line);
      const text = textLine.text;

      // Minimal YAML heuristic:
      //   subPath: some-file.yaml
      //   subPath: "some-file.yaml"
      //   subPath: 'some-file.yaml'
      const match = /^\s*subPath\s*:\s*("([^"]+)"|'([^']+)'|([^\s#]+))/.exec(text);
      if (!match) {
        continue;
      }

      const value = match[2] ?? match[3] ?? match[4];
      if (!value) {
        continue;
      }

      const valueStart = text.indexOf(value);
      if (valueStart < 0) {
        continue;
      }

      const range = new Range(line, valueStart, line, valueStart + value.length);
      const args = {
        uri: document.uri.toString(),
        position: { line, character: valueStart }
      };

      const cmdUri = Uri.parse(
        `command:k8sLsp.showSubPathTargets?${encodeURIComponent(JSON.stringify(args))}`
      );

      links.push(new DocumentLink(range, cmdUri));
    }

    return links;
  }
}

export function activate(context: ExtensionContext) {
  const outputChannel = window.createOutputChannel('Kubernetes LSP');
  outputChannel.appendLine('Activating Kubernetes LSP extension...');

  // Check if we are in development mode
  const isDev = context.extensionMode === 2; // ExtensionMode.Development

  let serverPath = workspace.getConfiguration('k8sLsp').get<string>('serverPath');
  
  if (!serverPath || serverPath === 'k8s-lsp') {
      if (isDev) {
          // In dev mode, look for binary in the root of the workspace
          serverPath = path.join(context.extensionPath, '..', 'k8s-lsp');
      } else {
          // In production, look for binary in the bin folder of the extension
          // We support linux, windows (win32), and macos (darwin) with different architectures
          const platform = process.platform;
          const arch = process.arch;
          const ext = platform === 'win32' ? '.exe' : '';
          serverPath = context.asAbsolutePath(path.join('bin', platform, arch, 'k8s-lsp' + ext));
      }
  }

  outputChannel.appendLine(`Extension mode: ${isDev ? 'development' : 'production'}`);
  outputChannel.appendLine(`Server path: ${serverPath}`);

  // Fail fast with a visible error if the server binary can't be found.
  if (!fs.existsSync(serverPath)) {
    const message = `k8s-lsp server binary not found at: ${serverPath}`;
    outputChannel.appendLine(message);
    window.showErrorMessage(message);
    outputChannel.show(true);
    return;
  }

  // If the extension is launched in debug mode then the debug server options are used
  // Otherwise the run options are used
  const serverOptions: ServerOptions = {
    run: { command: serverPath, transport: TransportKind.stdio },
    debug: { command: serverPath, transport: TransportKind.stdio }
  };

  // Options to control the language client
  const clientOptions: LanguageClientOptions = {
    // Register the server for plain text documents
    documentSelector: [{ scheme: 'file', language: 'yaml' }],
    initializationOptions: {
      crdSources: workspace.getConfiguration('k8sLsp').get<string[]>('crdSources') ?? []
    },
    synchronize: {
      // Notify the server about file changes to '.clientrc files contained in the workspace
      fileEvents: workspace.createFileSystemWatcher('**/*.{yaml,yml}')
    },
    outputChannel,
    revealOutputChannelOn: RevealOutputChannelOn.Error,
    middleware: {
      provideHover: async (document, position, token, next) => {
        const hover = (await next(document, position, token)) as Hover | null | undefined;
        if (!hover) {
          return hover;
        }

        const enableCommands = new Set([
          'k8sLsp.openEmbeddedFile',
          'k8sLsp.findEmbeddedFileUsages'
        ]);

        const trustMarkdown = (value: unknown) => {
          if (value instanceof MarkdownString) {
            value.isTrusted = { enabledCommands: Array.from(enableCommands) };
          }
        };

        if (Array.isArray(hover.contents)) {
          for (const c of hover.contents) {
            trustMarkdown(c);
          }
        } else {
          trustMarkdown(hover.contents);
        }

        return hover;
      }
    }
  };

  // Create the language client and start the client.
  client = new LanguageClient(
    'k8sLsp',
    'Kubernetes LSP',
    serverOptions,
    clientOptions
  );

  // Start the client. This will also launch the server
  try {
    client
      .start()
      .then(() => {
        const provider = new K8sFileSystemProvider(client);
        context.subscriptions.push(workspace.registerFileSystemProvider('k8s-embedded', provider, {
          isCaseSensitive: true,
          isReadonly: false
        }));

        context.subscriptions.push(
          commands.registerCommand('k8sLsp.openEmbeddedFile', async (args: any) => {
            const uriStr = typeof args === 'string' ? args : args?.uri;
            if (!uriStr) {
              return;
            }
            const uri = Uri.parse(uriStr);
            const doc = await workspace.openTextDocument(uri);
            await window.showTextDocument(doc, { preview: false });
          })
        );

        context.subscriptions.push(
          commands.registerCommand('k8sLsp.findEmbeddedFileUsages', async (args: any) => {
            const uriStr = args?.uri as string | undefined;
            const pos = args?.position as { line: number; character: number } | undefined;
            if (!uriStr || !pos) {
              return;
            }

            const uri = Uri.parse(uriStr);
            const position = new Position(pos.line, pos.character);

            // Ask the LSP server for references at the given position.
            const lspLocations = await client.sendRequest<any[]>('textDocument/references', {
              textDocument: { uri: uriStr },
              position: { line: pos.line, character: pos.character },
              context: { includeDeclaration: false }
            });

            const vscodeLocations = [] as any[];
            if (Array.isArray(lspLocations)) {
              for (const loc of lspLocations) {
                const converted = await client.protocol2CodeConverter.asLocation(loc);
                if (converted) {
                  vscodeLocations.push(converted);
                }
              }
            }

            if (vscodeLocations.length === 0) {
              return;
            }

            await commands.executeCommand('editor.action.showReferences', uri, position, vscodeLocations);
          })
        );

        context.subscriptions.push(
          commands.registerCommand('k8sLsp.showSubPathTargets', async (args: any) => {
            const uriStr = args?.uri as string | undefined;
            const pos = args?.position as { line: number; character: number } | undefined;
            if (!uriStr || !pos) {
              return;
            }

            const uri = Uri.parse(uriStr);
            const position = new Position(pos.line, pos.character);

            const lspLocations = await client.sendRequest<any[]>('textDocument/references', {
              textDocument: { uri: uriStr },
              position: { line: pos.line, character: pos.character },
              context: { includeDeclaration: false }
            });

            const vscodeLocations = [] as any[];
            if (Array.isArray(lspLocations)) {
              for (const loc of lspLocations) {
                const converted = await client.protocol2CodeConverter.asLocation(loc);
                if (converted) {
                  vscodeLocations.push(converted);
                }
              }
            }

            await commands.executeCommand('editor.action.showReferences', uri, position, vscodeLocations);
          })
        );

        context.subscriptions.push(
          languages.registerDocumentLinkProvider(
            [{ scheme: 'file', language: 'yaml' }],
            new SubPathDocumentLinkProvider()
          )
        );
      })
      .catch((err) => {
        outputChannel.appendLine(`Failed to start language client: ${String(err)}`);
        outputChannel.show(true);
      });
  } catch (err) {
    outputChannel.appendLine(`Exception while starting language client: ${String(err)}`);
    outputChannel.show(true);
  }
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) {
    return undefined;
  }
  return client.stop();
}
