package burp;

import java.nio.charset.StandardCharsets;
import java.util.List;

final class HttpMessageFormatter {
    private HttpMessageFormatter() {}

    static String getRequestTitle(IExtensionHelpers helpers, IHttpRequestResponse msg) {
        IRequestInfo reqInfo = helpers.analyzeRequest(msg);
        String method = reqInfo.getMethod();
        if (reqInfo.getUrl() == null) {
            return method + " (unknown)";
        }
        String host = reqInfo.getUrl().getHost();
        String path = reqInfo.getUrl().getPath();
        if (path == null || path.isEmpty()) path = "/";
        String query = reqInfo.getUrl().getQuery();
        String shortPath = path;
        if (shortPath.length() > 80) shortPath = shortPath.substring(0, 77) + "...";
        String q = (query != null && !query.isEmpty()) ? "?" : "";
        return method + " " + host + shortPath + q;
    }

    static String toPrompt(IExtensionHelpers helpers, IHttpRequestResponse msg) {
        IRequestInfo reqInfo = helpers.analyzeRequest(msg);
        String method = reqInfo.getMethod();
        String url = reqInfo.getUrl() != null ? reqInfo.getUrl().toString() : "(unknown)";

        byte[] reqBytes = msg.getRequest();
        int bodyOffset = reqInfo.getBodyOffset();
        String headers = String.join("\n", reqInfo.getHeaders());
        String body = "";
        if (reqBytes != null && reqBytes.length > bodyOffset) {
            body = new String(reqBytes, bodyOffset, reqBytes.length - bodyOffset, StandardCharsets.ISO_8859_1);
        }

        // Include response summary if available
        String respSnippet = "";
        byte[] respBytes = msg.getResponse();
        if (respBytes != null && respBytes.length > 0) {
            IResponseInfo respInfo = helpers.analyzeResponse(respBytes);
            List<String> respHeaders = respInfo.getHeaders();
            int respBodyOffset = respInfo.getBodyOffset();
            String respBody = "";
            if (respBytes.length > respBodyOffset) {
                int max = Math.min(respBytes.length - respBodyOffset, 4096);
                respBody = new String(respBytes, respBodyOffset, max, StandardCharsets.ISO_8859_1);
            }
            respSnippet = "\n\n[Optional: Response (truncated)]\n"
                    + String.join("\n", respHeaders)
                    + "\n\n"
                    + respBody;
        }

        return ""
                + "针对该流量做web渗透测试，并输出测试结果，要求：只针对该接口流量做测试，切勿拓展其他接口\n\n"
                + "[Target]\n"
                + method + " " + url + "\n\n"
                + "[Request]\n"
                + headers + "\n\n"
                + body
                + respSnippet;
    }
}

