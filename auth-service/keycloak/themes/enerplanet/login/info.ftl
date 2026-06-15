<#import "template.ftl" as layout>
<@layout.registrationLayout displayMessage=false; section>
    <#if section = "form">
        <#if messageHeader??>
            <h2>${kcSanitize(messageHeader)?no_esc}</h2>
        <#else>
            <h2>${message.summary}</h2>
        </#if>

        <#if requiredActions??>
            <div style="background-color: #f9fafb; border: 1px solid #e5e7eb; border-radius: 10px; padding: 16px 20px; margin: 16px 0;">
                <#list requiredActions as reqAction>
                    <div style="color: #111827; font-weight: 600; font-size: 15px; padding: 4px 0;">
                        ${msg("requiredAction.${reqAction}")}
                    </div>
                </#list>
            </div>
        <#else>
            <#if message.summary?has_content>
                <div class="alert alert-${message.type}" style="margin: 16px 0;">
                    <span>${kcSanitize(message.summary)?no_esc}</span>
                </div>
            </#if>
        </#if>

        <#if skipLink?? && skipLink>
        <#elseif pageRedirectUri?has_content>
            <p style="text-align: center; margin-top: 20px;">
                <a href="${pageRedirectUri}" style="display:inline-block;width:100%;background:linear-gradient(135deg,#1f2937,#111827);color:#fff;text-align:center;padding:14px 32px;border-radius:10px;font-weight:700;font-size:16px;text-decoration:none;">Continue</a>
            </p>
        <#elseif actionUri?has_content>
            <p style="text-align: center; margin-top: 20px;">
                <a href="${actionUri}" style="display:inline-block;width:100%;background:linear-gradient(135deg,#1f2937,#111827);color:#fff;text-align:center;padding:14px 32px;border-radius:10px;font-weight:700;font-size:16px;text-decoration:none;">Click here to proceed</a>
            </p>
        <#elseif client.baseUrl?has_content>
            <p style="text-align: center; margin-top: 20px;">
                <a href="${client.baseUrl}" style="display:inline-block;width:100%;background:linear-gradient(135deg,#1f2937,#111827);color:#fff;text-align:center;padding:14px 32px;border-radius:10px;font-weight:700;font-size:16px;text-decoration:none;">Back to Application</a>
            </p>
        </#if>
    </#if>
</@layout.registrationLayout>
