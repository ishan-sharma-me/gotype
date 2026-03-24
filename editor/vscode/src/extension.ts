import * as vscode from "vscode";
import {
  LanguageClient,
  LanguageClientOptions,
  ServerOptions,
  TransportKind,
} from "vscode-languageclient/node";

let client: LanguageClient;

export function activate(context: vscode.ExtensionContext) {
  const config = vscode.workspace.getConfiguration("gotype");
  const lspPath = config.get<string>("lspPath", "gotype-lsp");

  const serverOptions: ServerOptions = {
    command: lspPath,
    transport: TransportKind.stdio,
  };

  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: "file", language: "gotype" }],
    synchronize: {
      fileEvents: vscode.workspace.createFileSystemWatcher("**/*.tg"),
    },
  };

  client = new LanguageClient(
    "gotype",
    "GoType Language Server",
    serverOptions,
    clientOptions
  );

  client.start();
  context.subscriptions.push(client);
}

export function deactivate(): Thenable<void> | undefined {
  if (!client) return undefined;
  return client.stop();
}
