package burp;

import javax.swing.*;
import java.util.ArrayList;
import java.util.List;

public class BurpExtender implements IBurpExtender, IContextMenuFactory {
    private IBurpExtenderCallbacks callbacks;
    private IExtensionHelpers helpers;

    private CyberStrikeAITab tab;
    private final CyberStrikeAIClient client = new CyberStrikeAIClient();

    @Override
    public void registerExtenderCallbacks(IBurpExtenderCallbacks callbacks) {
        this.callbacks = callbacks;
        this.helpers = callbacks.getHelpers();

        callbacks.setExtensionName("CyberStrikeAI Extension");

        this.tab = new CyberStrikeAITab();
        callbacks.addSuiteTab(tab);

        callbacks.registerContextMenuFactory(this);

        callbacks.printOutput("CyberStrikeAI extension loaded.");
    }

    @Override
    public List<JMenuItem> createMenuItems(IContextMenuInvocation invocation) {
        List<JMenuItem> items = new ArrayList<>();

        JMenuItem sendItem = new JMenuItem("Send to CyberStrikeAI (stream test)");
        sendItem.addActionListener(e -> {
            IHttpRequestResponse[] selected = invocation.getSelectedMessages();
            if (selected == null || selected.length == 0) {
                return;
            }

            CyberStrikeAIClient.Config cfg = tab.currentConfig();
            String token = tab.getToken();
            if (token == null || token.trim().isEmpty()) {
                JOptionPane.showMessageDialog(tab.getUiComponent(),
                        "Please click Validate first to obtain a token.",
                        "CyberStrikeAI", JOptionPane.WARNING_MESSAGE);
                return;
            }

            String prompt = HttpMessageFormatter.toPrompt(helpers, selected[0]);
            String title = HttpMessageFormatter.getRequestTitle(helpers, selected[0]);
            String agentModeStr = (cfg.agentMode == CyberStrikeAIClient.AgentMode.MULTI) ? "Multi Agent" : "Single Agent";
            String runId = tab.startNewRun(title, agentModeStr, selected[0]);
            tab.appendProgressToRun(runId, "\n[server] " + cfg.baseUrl + "\n\n");

            client.streamTest(cfg, token, prompt, new CyberStrikeAIClient.StreamListener() {
                @Override
                public void onEvent(String type, String message, String rawJson) {
                    if (type == null) type = "";
                    switch (type) {
                        case "response_delta":
                        case "eino_agent_reply_stream_delta":
                            // delta chunk (content only)
                            tab.appendFinalToRun(runId, message);
                            break;
                        case "response":
                            // final response (full)
                            tab.appendFinalToRun(runId, "\n\n--- Final Response ---\n");
                            tab.appendFinalToRun(runId, message);
                            tab.setFinalResponse(runId, message);
                            break;
                        case "progress":
                            tab.appendProgressToRun(runId, "\n[progress] " + message + "\n");
                            tab.setRunStatus(runId, "running");
                            break;
                        case "cancelled":
                            tab.appendProgressToRun(runId, "\n[cancelled] " + message + "\n");
                            tab.setRunStatus(runId, "cancelled");
                            break;
                        case "error":
                            tab.appendProgressToRun(runId, "\n[error] " + message + "\n");
                            tab.setRunStatus(runId, "error");
                            break;
                        case "thinking_stream_start":
                            if (tab.isShowDebugEvents()) {
                                tab.resetThinkingStream(runId);
                            }
                            break;
                        case "thinking_stream_delta":
                        case "tool_call":
                        case "tool_result":
                        case "tool_result_delta":
                            // debug; hide by default
                            if (tab.isShowDebugEvents() && message != null && !message.isEmpty()) {
                                if ("thinking_stream_delta".equals(type)) {
                                    tab.appendThinkingDelta(runId, message);
                                } else {
                                    tab.appendProgressToRun(runId, "\n[" + type + "] " + message + "\n");
                                }
                            }
                            break;
                        case "conversation":
                            // Capture conversationId for stop/cancel.
                            if (rawJson != null) {
                                String convId = SimpleJson.extractStringField(rawJson, "conversationId");
                                if (convId != null && !convId.trim().isEmpty()) {
                                    tab.setRunConversationId(runId, convId);
                                }
                            }
                            if (tab.isShowDebugEvents() && message != null && !message.isEmpty()) {
                                tab.appendProgressToRun(runId, "\n[" + type + "] " + message + "\n");
                            }
                            break;
                        case "done":
                            // handled in onDone too
                            break;
                        default:
                            if (tab.isShowDebugEvents() && message != null && !message.isEmpty()) {
                                tab.appendProgressToRun(runId, "\n[" + type + "] " + message + "\n");
                            }
                            break;
                    }
                }

                @Override
                public void onError(String message, Exception e) {
                    tab.appendProgressToRun(runId, "\n[error] " + message + "\n");
                    tab.setRunStatus(runId, "error");
                    callbacks.printError("CyberStrikeAI stream error: " + message);
                    if (e != null) {
                        callbacks.printError(e.toString());
                    }
                }

                @Override
                public void onDone() {
                    tab.appendProgressToRun(runId, "\n\n[done]\n");
                    tab.setRunStatus(runId, "done");
                }
            });
        });

        items.add(sendItem);
        return items;
    }
}

